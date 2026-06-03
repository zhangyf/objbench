// Package runner executes the benchmark workload against an objstore.Bucket.
package runner

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thanos-io/objstore"

	"github.com/zhangyf/objbench/internal/config"
	"github.com/zhangyf/objbench/internal/stats"
	"github.com/zhangyf/objbench/internal/workload"
)

// Runner drives the benchmark for a single bucket.
type Runner struct {
	bkt objstore.Bucket
	cfg *config.Config
}

// New constructs a Runner.
func New(bkt objstore.Bucket, cfg *config.Config) *Runner {
	return &Runner{bkt: bkt, cfg: cfg}
}

// RunSize executes one duration-bounded workload for a given object size and
// returns the computed summary plus the list of keys created (for cleanup).
func (r *Runner) RunSize(ctx context.Context, size int64) (stats.Summary, []string, error) {
	payload, err := workload.NewPayload(size)
	if err != nil {
		return stats.Summary{}, nil, fmt.Errorf("alloc payload: %w", err)
	}

	collector := stats.NewCollector()

	// Track keys we create so reads can target real objects and cleanup works.
	var keyMu sync.Mutex
	keys := make([]string, 0, 1024)
	addKey := func(k string) {
		keyMu.Lock()
		keys = append(keys, k)
		keyMu.Unlock()
	}
	randKey := func(rng *rand.Rand) (string, bool) {
		keyMu.Lock()
		defer keyMu.Unlock()
		if len(keys) == 0 {
			return "", false
		}
		return keys[rng.Intn(len(keys))], true
	}

	// Warmup: pre-upload objects so downloads have targets when ReadRatio > 0.
	warm := r.cfg.WarmupObjects
	if warm <= 0 {
		warm = r.cfg.Concurrency
	}
	if r.cfg.ReadRatio > 0 {
		for i := 0; i < warm; i++ {
			key := r.keyFor(size, "warm", i)
			if err := r.bkt.Upload(ctx, key, payload.Reader()); err != nil {
				return stats.Summary{}, keys, fmt.Errorf("warmup upload: %w", err)
			}
			addKey(key)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()

	var seq int64
	start := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < r.cfg.Concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*7919))

			for {
				if runCtx.Err() != nil {
					return
				}

				isRead := rng.Float64() < r.cfg.ReadRatio
				if isRead {
					key, ok := randKey(rng)
					if !ok {
						// No object yet; fall back to a write.
						isRead = false
					} else {
						r.doDownload(runCtx, collector, key)
						continue
					}
				}

				n := atomic.AddInt64(&seq, 1)
				key := r.keyFor(size, "w", int(n))
				if r.doUpload(runCtx, collector, key, payload.Reader(), size) {
					addKey(key)
				}
			}
		}(w)
	}

	wg.Wait()
	wall := time.Since(start)

	return stats.Compute(collector.Snapshot(), wall), keys, nil
}

func (r *Runner) doUpload(ctx context.Context, c *stats.Collector, key string, body io.Reader, size int64) bool {
	t0 := time.Now()
	err := r.bkt.Upload(ctx, key, body)
	lat := time.Since(t0)
	if ctx.Err() != nil && err != nil {
		// Timeout cancellation; don't record a misleading sample.
		return false
	}
	c.Record(stats.Sample{
		Kind:    stats.OpUpload,
		Latency: lat,
		Bytes:   size,
		Err:     err != nil,
	})
	return err == nil
}

func (r *Runner) doDownload(ctx context.Context, c *stats.Collector, key string) {
	t0 := time.Now()
	rc, err := r.bkt.Get(ctx, key)
	var n int64
	if err == nil {
		n, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	lat := time.Since(t0)
	if ctx.Err() != nil && err != nil {
		return
	}
	c.Record(stats.Sample{
		Kind:    stats.OpDownload,
		Latency: lat,
		Bytes:   n,
		Err:     err != nil,
	})
}

func (r *Runner) keyFor(size int64, tag string, i int) string {
	return fmt.Sprintf("%sobjbench/sz-%d/%s-%d", r.cfg.KeyPrefix, size, tag, i)
}

// Cleanup deletes the given keys, ignoring not-found errors.
func (r *Runner) Cleanup(ctx context.Context, keys []string) {
	for _, k := range keys {
		_ = r.bkt.Delete(ctx, k)
	}
}
