package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/zhangyf/objstore"

	"github.com/zhangyf/objbench/internal/config"
	"github.com/zhangyf/objbench/internal/coord"
	"github.com/zhangyf/objbench/internal/stats"
)

func runCoordinate(args []string) error {
	fs := flag.NewFlagSet("objbench coordinate", flag.ExitOnError)
	cf := registerCoordFlags(fs)
	var (
		runID        = fs.String("run-id", "", "run id (default: timestamp-based)")
		sizesStr     = fs.String("sizes", "4k,64k,1m,8m", "comma-separated object sizes")
		duration     = fs.Duration("duration", 30*time.Second, "test duration per size group; Go duration with unit, e.g. 30s, 5m, 1h30m")
		concurrency  = fs.Int("concurrency", 16, "per-agent parallel workers")
		readRatio    = fs.Float64("read-ratio", 0.5, "fraction of reads in [0,1]")
		perAgentRate = fs.Float64("rate", 0, "per-agent QPS ceiling (0 = unlimited); cluster ≈ rate × agents")
		burst        = fs.Int("burst", 1, "per-agent token-bucket burst")
		keyPrefix    = fs.String("prefix", "", "object key prefix for the run")
		cleanup      = fs.Bool("cleanup", true, "agents delete objects they create")
		warmup       = fs.Int("warmup", 0, "objects pre-uploaded per size per agent (0 = concurrency)")
		startDelay   = fs.Duration("start-delay", 30*time.Second, "lead time before coordinated start (agents must join within this); Go duration with unit, e.g. 30s, 2m")
		expectAgents = fs.Int("expect-agents", 0, "if >0, wait until this many agents register before computing start (informational)")
		collectWait  = fs.Duration("collect-timeout", 10*time.Minute, "max time to wait for all agent results; Go duration with unit, e.g. 30s, 10m, 1h")
		expectResults = fs.Int("expect-results", 0, "stop collecting once this many results arrive (0 = wait full timeout / all registered)")
	)
	_ = fs.Parse(args)

	sizes, err := config.ParseSizes(*sizesStr)
	if err != nil {
		return err
	}

	rid := *runID
	if rid == "" {
		rid = fmt.Sprintf("run-%d", time.Now().Unix())
	}

	coordCfg := cf.resolveStoreConfig()
	if coordCfg.Bucket == "" {
		return fmt.Errorf("-coord-bucket is required (dedicated coordination bucket)")
	}
	coordStore, err := objstore.New(coordCfg)
	if err != nil {
		return fmt.Errorf("create coordination store: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installSignalCancel(cancel)

	cc := coord.NewClient(coordStore, *cf.prefix)

	fmt.Printf("coordinator — run-id=%s coord=%s/%s\n", rid, coordStore.Provider(), coordStore.BucketName())

	// Optionally wait for an expected number of agents to register before
	// locking the start time (purely informational for A-mode).
	if *expectAgents > 0 {
		fmt.Printf("waiting for %d agents to register (lead %s)...\n", *expectAgents, *startDelay)
		deadline := time.Now().Add(*startDelay)
		for time.Now().Before(deadline) {
			ags, _ := cc.ListAgents(ctx)
			if len(ags) >= *expectAgents {
				fmt.Printf("%d agents registered.\n", len(ags))
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	startAt := time.Now().Add(*startDelay)
	plan := &coord.Plan{
		RunID:         rid,
		SizesB:        sizes,
		DurationNs:    int64(*duration),
		PerAgentRate:  *perAgentRate,
		Burst:         *burst,
		Concurrency:   *concurrency,
		ReadRatio:     *readRatio,
		KeyPrefix:     *keyPrefix,
		Cleanup:       *cleanup,
		WarmupObjects: *warmup,
		StartAtUnixMs: startAt.UnixMilli(),
	}
	if err := cc.PublishPlan(ctx, plan); err != nil {
		return fmt.Errorf("publish plan: %w", err)
	}
	fmt.Printf("plan published: sizes=%d duration=%s per-agent-rate=%.0f start=%s\n",
		len(sizes), *duration, *perAgentRate, startAt.Format(time.RFC3339))
	fmt.Println("agents that join before start will participate. Total load = per-agent-rate × #agents.")

	// Estimated time agents finish all size groups.
	estRun := time.Duration(len(sizes)) * (*duration)
	fmt.Printf("collecting results (est. run ~%s)...\n", estRun)

	results, err := collectResults(ctx, cc, rid, *collectWait, *expectResults)
	if err != nil {
		return fmt.Errorf("collect results: %w", err)
	}
	if len(results) == 0 {
		return fmt.Errorf("no agent results received")
	}

	printGlobalReport(rid, sizes, *duration, results)
	return nil
}

func collectResults(ctx context.Context, cc *coord.Client, runID string, timeout time.Duration, expect int) ([]coord.AgentResult, error) {
	deadline := time.Now().Add(timeout)
	seen := map[string]coord.AgentResult{}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		rs, err := cc.ListResults(ctx, runID)
		if err == nil {
			for _, r := range rs {
				seen[r.AgentID] = r
			}
		}
		if expect > 0 && len(seen) >= expect {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
	out := make([]coord.AgentResult, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	return out, nil
}

// printGlobalReport aggregates all agents' per-size reports and prints a
// cluster-wide summary per size group.
func printGlobalReport(runID string, sizes []int64, dur time.Duration, results []coord.AgentResult) {
	agentIDs := make([]string, 0, len(results))
	for _, r := range results {
		agentIDs = append(agentIDs, r.AgentID)
	}
	sort.Strings(agentIDs)

	fmt.Printf("\n================ GLOBAL REPORT  run=%s ================\n", runID)
	fmt.Printf("agents:       %d  (%v)\n", len(results), agentIDs)
	fmt.Printf("duration:     %s per size (coordinated window)\n", dur)
	fmt.Printf("sizes:        %d groups\n", len(sizes))

	// Group reports by size.
	for _, size := range sizes {
		var group []stats.AgentReport
		for _, r := range results {
			for _, rep := range r.Reports {
				if rep.SizeB == size {
					group = append(group, rep)
				}
			}
		}
		if len(group) == 0 {
			continue
		}
		summary := stats.MergeReports(group, dur)
		stats.WriteReport(os.Stdout, sizeLabel(size)+fmt.Sprintf(" (cluster, %d agents)", len(group)), summary)
	}
}
