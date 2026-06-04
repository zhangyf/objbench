package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhangyf/objstore"

	"github.com/zhangyf/objbench/internal/config"
	"github.com/zhangyf/objbench/internal/runner"
	"github.com/zhangyf/objbench/internal/stats"
)

// storeFlags holds the common store/credential flags shared by all modes.
type storeFlags struct {
	provider  *string
	bucket    *string
	region    *string
	secretID  *string
	secretKey *string
	endpoint  *string
	profile   *string
}

func registerStoreFlags(fs *flag.FlagSet) *storeFlags {
	return &storeFlags{
		provider:  fs.String("provider", "cos", "storage provider: cos | s3"),
		bucket:    fs.String("bucket", "", "bucket name (required)"),
		region:    fs.String("region", "", "region, e.g. ap-beijing / ap-northeast-1"),
		secretID:  fs.String("secret-id", "", "COS SecretId / S3 Access Key ID (falls back to env if empty)"),
		secretKey: fs.String("secret-key", "", "COS SecretKey / S3 Secret Access Key (falls back to env if empty)"),
		endpoint:  fs.String("endpoint", "", "custom endpoint (S3-compatible)"),
		profile:   fs.String("profile", "", "AWS profile (S3 only, when keys empty)"),
	}
}

// resolveStoreConfig applies env-variable credential fallback and returns an
// objstore.Config. Command-line flags take precedence; empty falls back to
// env based on provider.
func (sf *storeFlags) resolveStoreConfig() objstore.Config {
	sid, skey := *sf.secretID, *sf.secretKey
	switch objstore.ProviderType(*sf.provider) {
	case objstore.ProviderCOS:
		if sid == "" {
			sid = firstNonEmpty(os.Getenv("OBJBENCH_SECRET_ID"), os.Getenv("COS_SECRET_ID"))
		}
		if skey == "" {
			skey = firstNonEmpty(os.Getenv("OBJBENCH_SECRET_KEY"), os.Getenv("COS_SECRET_KEY"))
		}
	case objstore.ProviderS3:
		if sid == "" {
			sid = firstNonEmpty(os.Getenv("OBJBENCH_SECRET_ID"), os.Getenv("AWS_ACCESS_KEY_ID"))
		}
		if skey == "" {
			skey = firstNonEmpty(os.Getenv("OBJBENCH_SECRET_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
		}
	}
	bucket := *sf.bucket
	if bucket == "" {
		bucket = os.Getenv("OBJBENCH_BUCKET")
	}
	region := *sf.region
	if region == "" {
		region = os.Getenv("OBJBENCH_REGION")
	}
	return objstore.Config{
		Provider:  objstore.ProviderType(*sf.provider),
		Bucket:    bucket,
		Region:    region,
		SecretID:  sid,
		SecretKey: skey,
		Endpoint:  *sf.endpoint,
		Profile:   *sf.profile,
	}
}

func runSingle(args []string) error {
	fs := flag.NewFlagSet("objbench", flag.ExitOnError)
	sf := registerStoreFlags(fs)
	var (
		sizesStr    = fs.String("sizes", "4k,64k,1m,8m", "comma-separated object sizes, e.g. 4k,1m,16m")
		duration    = fs.Duration("duration", 30*time.Second, "test duration per size group; Go duration with unit, e.g. 30s, 5m, 1h30m")
		concurrency = fs.Int("concurrency", 16, "number of parallel workers")
		readRatio   = fs.Float64("read-ratio", 0.5, "fraction of reads in [0,1]; rest are writes")
		keyPrefix   = fs.String("prefix", "", "key prefix for all benchmark objects")
		cleanup     = fs.Bool("cleanup", true, "delete objects created during the run")
		warmup      = fs.Int("warmup", 0, "objects pre-uploaded per size (0 = concurrency)")
		ratePerSec  = fs.Float64("rate", 0, "target QPS ceiling (token bucket); 0 = unlimited (saturation)")
		burst       = fs.Int("burst", 1, "token-bucket burst size; small = smoother pacing")
	)
	_ = fs.Parse(args)

	sizes, err := config.ParseSizes(*sizesStr)
	if err != nil {
		return err
	}

	cfg := &config.Config{
		Store:         sf.resolveStoreConfig(),
		Sizes:         sizes,
		Duration:      *duration,
		Concurrency:   *concurrency,
		ReadRatio:     *readRatio,
		KeyPrefix:     *keyPrefix,
		Cleanup:       *cleanup,
		WarmupObjects: *warmup,
		Rate:          *ratePerSec,
		Burst:         *burst,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	store, err := objstore.New(cfg.Store)
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installSignalCancel(cancel)

	printHeader(cfg, store)

	r := runner.New(store, cfg)
	for _, size := range sizes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		summary, keys, err := r.RunSize(ctx, size)
		if err != nil {
			return fmt.Errorf("run size %d: %w", size, err)
		}
		stats.WriteReport(os.Stdout, sizeLabel(size), summary)

		if cfg.Cleanup && len(keys) > 0 {
			cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
			r.Cleanup(cctx, keys)
			ccancel()
		}
	}
	return nil
}

func installSignalCancel(cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\ninterrupt received, stopping...")
		cancel()
	}()
}

func printHeader(cfg *config.Config, store objstore.Store) {
	fmt.Printf("objbench — objstore performance benchmark\n")
	fmt.Printf("provider:     %s\n", store.Provider())
	fmt.Printf("bucket:       %s\n", store.BucketName())
	fmt.Printf("duration:     %s per size\n", cfg.Duration)
	fmt.Printf("concurrency:  %d\n", cfg.Concurrency)
	fmt.Printf("read-ratio:   %.2f (writes %.2f)\n", cfg.ReadRatio, 1-cfg.ReadRatio)
	if cfg.Rate > 0 {
		fmt.Printf("rate:         %.0f QPS (burst %d)\n", cfg.Rate, cfg.Burst)
	} else {
		fmt.Printf("rate:         unlimited (saturation)\n")
	}
	fmt.Printf("sizes:        %d groups\n", len(cfg.Sizes))
}

func sizeLabel(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("size=%dGiB", size>>30)
	case size >= 1<<20:
		return fmt.Sprintf("size=%dMiB", size>>20)
	case size >= 1<<10:
		return fmt.Sprintf("size=%dKiB", size>>10)
	default:
		return fmt.Sprintf("size=%dB", size)
	}
}
