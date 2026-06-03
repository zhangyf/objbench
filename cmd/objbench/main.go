// Command objbench is an object-storage performance benchmark built on the
// github.com/zhangyf/objstore interface library. It measures upload/download
// latency across configurable object sizes, supports a fixed test duration and
// a read/write mix, and reports QPS, throughput, and P90/P95/P99 latencies.
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "objbench: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		provider    = flag.String("provider", "cos", "storage provider: cos | s3")
		bucket      = flag.String("bucket", "", "bucket name (required)")
		region      = flag.String("region", "", "region, e.g. ap-beijing / ap-northeast-1")
		secretID    = flag.String("secret-id", "", "COS SecretId / S3 Access Key ID (falls back to env if empty)")
		secretKey   = flag.String("secret-key", "", "COS SecretKey / S3 Secret Access Key (falls back to env if empty)")
		endpoint    = flag.String("endpoint", "", "custom endpoint (S3-compatible)")
		profile     = flag.String("profile", "", "AWS profile (S3 only, when keys empty)")
		sizesStr    = flag.String("sizes", "4k,64k,1m,8m", "comma-separated object sizes, e.g. 4k,1m,16m")
		duration    = flag.Duration("duration", 30*time.Second, "test duration per size group")
		concurrency = flag.Int("concurrency", 16, "number of parallel workers")
		readRatio   = flag.Float64("read-ratio", 0.5, "fraction of reads in [0,1]; rest are writes")
		keyPrefix   = flag.String("prefix", "", "key prefix for all benchmark objects")
		cleanup     = flag.Bool("cleanup", true, "delete objects created during the run")
		warmup      = flag.Int("warmup", 0, "objects pre-uploaded per size (0 = concurrency)")
	)
	flag.Parse()

	sizes, err := config.ParseSizes(*sizesStr)
	if err != nil {
		return err
	}

	// Credential resolution: command-line flags take precedence; when empty,
	// fall back to environment variables based on provider.
	//   generic:  OBJBENCH_SECRET_ID / OBJBENCH_SECRET_KEY
	//   cos:      COS_SECRET_ID      / COS_SECRET_KEY
	//   s3:       AWS_ACCESS_KEY_ID  / AWS_SECRET_ACCESS_KEY
	// For S3, leaving both empty lets objstore use the AWS default credential
	// chain (env / shared config / IMDS / STS), so we do not force-read them.
	sid, skey := *secretID, *secretKey
	switch objstore.ProviderType(*provider) {
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
	if *bucket == "" {
		*bucket = os.Getenv("OBJBENCH_BUCKET")
	}
	if *region == "" {
		*region = os.Getenv("OBJBENCH_REGION")
	}

	cfg := &config.Config{
		Store: objstore.Config{
			Provider:  objstore.ProviderType(*provider),
			Bucket:    *bucket,
			Region:    *region,
			SecretID:  sid,
			SecretKey: skey,
			Endpoint:  *endpoint,
			Profile:   *profile,
		},
		Sizes:         sizes,
		Duration:      *duration,
		Concurrency:   *concurrency,
		ReadRatio:     *readRatio,
		KeyPrefix:     *keyPrefix,
		Cleanup:       *cleanup,
		WarmupObjects: *warmup,
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

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\ninterrupt received, stopping...")
		cancel()
	}()

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

func printHeader(cfg *config.Config, store objstore.Store) {
	fmt.Printf("objbench — objstore performance benchmark\n")
	fmt.Printf("provider:     %s\n", store.Provider())
	fmt.Printf("bucket:       %s\n", store.BucketName())
	fmt.Printf("duration:     %s per size\n", cfg.Duration)
	fmt.Printf("concurrency:  %d\n", cfg.Concurrency)
	fmt.Printf("read-ratio:   %.2f (writes %.2f)\n", cfg.ReadRatio, 1-cfg.ReadRatio)
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

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
