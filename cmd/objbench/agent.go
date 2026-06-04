package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/zhangyf/objstore"

	"github.com/zhangyf/objbench/internal/config"
	"github.com/zhangyf/objbench/internal/coord"
	"github.com/zhangyf/objbench/internal/runner"
	"github.com/zhangyf/objbench/internal/stats"
)

// coordFlags holds the dedicated coordination-bucket flags (the bulletin
// board), kept separate from the bucket under test.
type coordFlags struct {
	provider  *string
	bucket    *string
	region    *string
	secretID  *string
	secretKey *string
	endpoint  *string
	profile   *string
	prefix    *string
}

func registerCoordFlags(fs *flag.FlagSet) *coordFlags {
	return &coordFlags{
		provider:  fs.String("coord-provider", "cos", "coordination bucket provider: cos | s3"),
		bucket:    fs.String("coord-bucket", "", "coordination bucket (dedicated, separate from target; required)"),
		region:    fs.String("coord-region", "", "coordination bucket region"),
		secretID:  fs.String("coord-secret-id", "", "coordination bucket SecretId/AccessKey (env fallback)"),
		secretKey: fs.String("coord-secret-key", "", "coordination bucket SecretKey (env fallback)"),
		endpoint:  fs.String("coord-endpoint", "", "coordination bucket custom endpoint"),
		profile:   fs.String("coord-profile", "", "coordination bucket AWS profile"),
		prefix:    fs.String("coord-prefix", "objbench-coord", "key prefix for coordination objects"),
	}
}

func (cf *coordFlags) resolveStoreConfig() objstore.Config {
	sid, skey := *cf.secretID, *cf.secretKey
	switch objstore.ProviderType(*cf.provider) {
	case objstore.ProviderCOS:
		if sid == "" {
			sid = firstNonEmpty(os.Getenv("OBJBENCH_COORD_SECRET_ID"), os.Getenv("COS_SECRET_ID"))
		}
		if skey == "" {
			skey = firstNonEmpty(os.Getenv("OBJBENCH_COORD_SECRET_KEY"), os.Getenv("COS_SECRET_KEY"))
		}
	case objstore.ProviderS3:
		if sid == "" {
			sid = firstNonEmpty(os.Getenv("OBJBENCH_COORD_SECRET_ID"), os.Getenv("AWS_ACCESS_KEY_ID"))
		}
		if skey == "" {
			skey = firstNonEmpty(os.Getenv("OBJBENCH_COORD_SECRET_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
		}
	}
	bucket := *cf.bucket
	if bucket == "" {
		bucket = os.Getenv("OBJBENCH_COORD_BUCKET")
	}
	region := *cf.region
	if region == "" {
		region = os.Getenv("OBJBENCH_COORD_REGION")
	}
	return objstore.Config{
		Provider:  objstore.ProviderType(*cf.provider),
		Bucket:    bucket,
		Region:    region,
		SecretID:  sid,
		SecretKey: skey,
		Endpoint:  *cf.endpoint,
		Profile:   *cf.profile,
	}
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("objbench agent", flag.ExitOnError)
	sf := registerStoreFlags(fs)   // target bucket (under test)
	cf := registerCoordFlags(fs)   // coordination bucket (bulletin board)
	var (
		agentID  = fs.String("agent-id", "", "unique agent id (default: hostname-pid)")
		runID    = fs.String("run-id", "", "run id to join (default: whatever the plan says)")
		pollIntv = fs.Duration("poll", 2*time.Second, "plan polling interval; Go duration with unit, e.g. 500ms, 2s, 1m")
		waitFor  = fs.Duration("wait-timeout", 10*time.Minute, "max time to wait for a plan; Go duration with unit, e.g. 30s, 10m, 1h")
	)
	_ = fs.Parse(args)

	host, _ := os.Hostname()
	id := *agentID
	if id == "" {
		id = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	// Target store (under test).
	targetCfg := sf.resolveStoreConfig()
	if targetCfg.Bucket == "" {
		return fmt.Errorf("target -bucket is required")
	}
	target, err := objstore.New(targetCfg)
	if err != nil {
		return fmt.Errorf("create target store: %w", err)
	}

	// Coordination store (bulletin board).
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

	fmt.Printf("agent %s — target=%s/%s coord=%s/%s\n",
		id, target.Provider(), target.BucketName(),
		coordStore.Provider(), coordStore.BucketName())

	// Register presence (best-effort informational).
	if err := cc.Register(ctx, coord.AgentRegistration{AgentID: id, Host: host}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: register failed: %v\n", err)
	}

	// Wait for the plan.
	fmt.Println("waiting for plan...")
	wctx, wcancel := context.WithTimeout(ctx, *waitFor)
	plan, err := cc.WaitForPlan(wctx, *pollIntv)
	wcancel()
	if err != nil {
		return fmt.Errorf("waiting for plan: %w", err)
	}
	if *runID != "" && plan.RunID != *runID {
		return fmt.Errorf("plan run-id %q != requested %q", plan.RunID, *runID)
	}
	fmt.Printf("got plan run-id=%s sizes=%d duration=%s per-agent-rate=%.0f start=%s\n",
		plan.RunID, len(plan.SizesB), plan.Duration(), plan.PerAgentRate,
		plan.StartAt().Format(time.RFC3339))

	// Lockstep start: wait until the shared absolute instant.
	now := time.Now()
	start := plan.StartAt()
	if d := time.Until(start); d > 0 {
		fmt.Printf("sleeping %s until coordinated start...\n", d.Truncate(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	} else {
		fmt.Printf("warn: start time already passed by %s (clock skew or late join); starting now\n", now.Sub(start).Truncate(time.Millisecond))
	}

	// Build the per-agent run config from the plan.
	cfg := &config.Config{
		Store:         targetCfg,
		Sizes:         plan.SizesB,
		Duration:      plan.Duration(),
		Concurrency:   plan.Concurrency,
		ReadRatio:     plan.ReadRatio,
		KeyPrefix:     fmt.Sprintf("%s%s/%s/", ensureSlash(plan.KeyPrefix), plan.RunID, id),
		Cleanup:       plan.Cleanup,
		WarmupObjects: plan.WarmupObjects,
		Rate:          plan.PerAgentRate,
		Burst:         plan.Burst,
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid plan-derived config: %w", err)
	}

	r := runner.New(target, cfg)
	result := coord.AgentResult{AgentID: id, RunID: plan.RunID}

	for _, size := range plan.SizesB {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		fmt.Printf("running size=%d...\n", size)
		samples, keys, wall, err := r.RunSizeRaw(ctx, size)
		if err != nil {
			return fmt.Errorf("run size %d: %w", size, err)
		}
		rep := stats.BuildAgentReport(id, size, samples, wall)
		result.Reports = append(result.Reports, rep)

		// Local quick view.
		stats.WriteReport(os.Stdout, sizeLabel(size)+" (this agent)", stats.Compute(samples, wall))

		if cfg.Cleanup && len(keys) > 0 {
			cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
			r.Cleanup(cctx, keys)
			ccancel()
		}
	}

	// Upload result to the bulletin board (post-load; bucket is idle now).
	fmt.Println("uploading result...")
	if err := cc.UploadResult(context.Background(), result); err != nil {
		return fmt.Errorf("upload result: %w", err)
	}
	fmt.Printf("agent %s done; result uploaded.\n", id)
	return nil
}

func ensureSlash(s string) string {
	if s == "" {
		return ""
	}
	if s[len(s)-1] != '/' {
		return s + "/"
	}
	return s
}
