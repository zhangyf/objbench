// Package coord implements bucket-as-bulletin-board coordination for
// distributed objbench runs. A coordinator publishes a Plan and collects
// per-agent results; agents register, wait for the shared start time, run, and
// upload results. All cross-node communication goes through an objstore.Store
// (ideally a small dedicated bucket, physically separate from the bucket under
// test) so no extra service, port, or network path is required.
//
// Reads/writes here happen only BEFORE and AFTER the load phase, never during
// it, so backend throttling on the target bucket does not affect pacing. Calls
// are still retried with backoff for robustness.
package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/zhangyf/objstore"

	"github.com/zhangyf/objbench/internal/stats"
)

// Layout of coordination objects under a common prefix.
const (
	planObject    = "control/plan.json"
	agentsPrefix  = "agents/"
	resultsPrefix = "results/"
)

// Plan is published by the coordinator and read by every agent. It describes
// WHAT to run and WHEN to start, but NOT how many agents participate — total
// cluster load = per-agent quota × number of agents that show up (A-mode:
// add a machine to add load, no plan change required).
type Plan struct {
	RunID string `json:"runID"`

	// SizesB is the list of object sizes (bytes) to benchmark, in order.
	SizesB []int64 `json:"sizesB"`

	// DurationNs is the per-size test window each agent runs.
	DurationNs int64 `json:"durationNs"`

	// PerAgentRate is the token-bucket QPS ceiling for EACH agent (0 =
	// unlimited). Cluster target ≈ PerAgentRate × agentCount.
	PerAgentRate float64 `json:"perAgentRate"`
	// Burst is the token-bucket burst for each agent (0 => 1, smooth pacing).
	Burst int `json:"burst"`

	Concurrency int     `json:"concurrency"`
	ReadRatio   float64 `json:"readRatio"`
	KeyPrefix   string  `json:"keyPrefix"`
	Cleanup     bool    `json:"cleanup"`
	WarmupObjects int   `json:"warmupObjects"`

	// StartAtUnixMs is the absolute wall-clock instant (ms since epoch) all
	// agents begin the FIRST size group. Agents wait until this moment, so
	// they start in lockstep without direct messaging (clock-sync via NTP).
	StartAtUnixMs int64 `json:"startAtUnixMs"`

	// PublishedAtUnixMs records when the coordinator wrote the plan.
	PublishedAtUnixMs int64 `json:"publishedAtUnixMs"`
}

// Duration returns the per-size window as a time.Duration.
func (p *Plan) Duration() time.Duration { return time.Duration(p.DurationNs) }

// StartAt returns the coordinated start instant.
func (p *Plan) StartAt() time.Time {
	return time.UnixMilli(p.StartAtUnixMs)
}

// AgentRegistration is written by each agent at startup so the coordinator can
// see who showed up (best-effort, informational).
type AgentRegistration struct {
	AgentID         string `json:"agentID"`
	Host            string `json:"host"`
	RegisteredAtMs  int64  `json:"registeredAtMs"`
}

// AgentResult is what an agent uploads after finishing all size groups.
type AgentResult struct {
	AgentID string              `json:"agentID"`
	RunID   string              `json:"runID"`
	Reports []stats.AgentReport `json:"reports"` // one per size group
}

// Client wraps an objstore.Store with a coordination key prefix and retry.
type Client struct {
	store  objstore.Store
	prefix string
}

// NewClient builds a coordination client. prefix scopes all coordination
// objects (e.g. "objbench-coord/<runID>/").
func NewClient(store objstore.Store, prefix string) *Client {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Client{store: store, prefix: prefix}
}

func (c *Client) key(parts ...string) string {
	return c.prefix + path.Join(parts...)
}

// --- Coordinator side ---------------------------------------------------

// PublishPlan writes the plan to the bulletin board.
func (c *Client) PublishPlan(ctx context.Context, p *Plan) error {
	p.PublishedAtUnixMs = time.Now().UnixMilli()
	return c.putJSON(ctx, c.key(planObject), p)
}

// ListResults reads all uploaded agent results for the given run.
func (c *Client) ListResults(ctx context.Context, runID string) ([]AgentResult, error) {
	keys, err := c.list(ctx, c.key(resultsPrefix))
	if err != nil {
		return nil, err
	}
	var out []AgentResult
	for _, k := range keys {
		var r AgentResult
		if err := c.getJSON(ctx, k, &r); err != nil {
			continue // skip partial/garbage objects
		}
		if runID == "" || r.RunID == runID {
			out = append(out, r)
		}
	}
	return out, nil
}

// ListAgents reads agent registrations (informational).
func (c *Client) ListAgents(ctx context.Context) ([]AgentRegistration, error) {
	keys, err := c.list(ctx, c.key(agentsPrefix))
	if err != nil {
		return nil, err
	}
	var out []AgentRegistration
	for _, k := range keys {
		var a AgentRegistration
		if err := c.getJSON(ctx, k, &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// --- Agent side ---------------------------------------------------------

// Register announces this agent's presence.
func (c *Client) Register(ctx context.Context, reg AgentRegistration) error {
	reg.RegisteredAtMs = time.Now().UnixMilli()
	return c.putJSON(ctx, c.key(agentsPrefix, reg.AgentID+".json"), reg)
}

// WaitForPlan polls the bulletin board until a plan appears or ctx is done.
func (c *Client) WaitForPlan(ctx context.Context, poll time.Duration) (*Plan, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		var p Plan
		if err := c.getJSON(ctx, c.key(planObject), &p); err == nil && p.RunID != "" {
			return &p, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// UploadResult writes this agent's final result.
func (c *Client) UploadResult(ctx context.Context, r AgentResult) error {
	return c.putJSON(ctx, c.key(resultsPrefix, r.AgentID+".json"), r)
}

// --- low-level helpers with retry --------------------------------------

func (c *Client) putJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.retry(ctx, func() error {
		return c.store.PutObjectStream(ctx, key, bytes.NewReader(b), int64(len(b)))
	})
}

func (c *Client) getJSON(ctx context.Context, key string, v any) error {
	var data []byte
	err := c.retry(ctx, func() error {
		rc, err := c.store.GetObject(ctx, key)
		if err != nil {
			return err
		}
		defer rc.Close()
		data, err = io.ReadAll(rc)
		return err
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (c *Client) list(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := c.retry(ctx, func() error {
		infos, err := c.store.ListObjects(ctx, objstore.ListOptions{Prefix: prefix, Delimiter: ""})
		if err != nil {
			return err
		}
		ks := make([]string, 0, len(infos))
		for _, in := range infos {
			ks = append(ks, in.Key)
		}
		keys = ks
		return nil
	})
	return keys, err
}

// retry runs fn with bounded exponential backoff. Coordination calls happen
// off the hot path, so a few retries cheaply absorb transient throttling.
func (c *Client) retry(ctx context.Context, fn func() error) error {
	const maxAttempts = 5
	backoff := 200 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
	return fmt.Errorf("coordination call failed after %d attempts: %w", maxAttempts, lastErr)
}
