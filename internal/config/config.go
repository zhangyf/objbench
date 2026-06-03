// Package config defines the runtime configuration for objbench.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyf/objstore"
)

// Config holds all benchmark parameters.
type Config struct {
	// Store is the objstore connection configuration (provider/bucket/creds).
	Store objstore.Config

	// Sizes is the list of object sizes (in bytes) to benchmark.
	Sizes []int64

	// Duration is the total test duration per size group.
	Duration time.Duration

	// Concurrency is the number of parallel workers.
	Concurrency int

	// ReadRatio is the fraction of operations that are reads (downloads),
	// in range [0,1]. The remainder are writes (uploads).
	ReadRatio float64

	// KeyPrefix is prepended to every object key created by the benchmark.
	KeyPrefix string

	// Cleanup removes objects created during the run when true.
	Cleanup bool

	// WarmupObjects is the number of objects pre-uploaded per size so that
	// read operations have something to fetch. If 0, defaults to Concurrency.
	WarmupObjects int

	// Rate is the target operations-per-second ceiling for THIS process
	// (token-bucket limiter). 0 means unlimited (saturation benchmark).
	// In distributed mode this is the per-agent quota.
	Rate float64

	// Burst is the token-bucket burst size. Defaults to 1 for a smooth,
	// evenly-paced request stream (recommended for clean latency data).
	Burst int
}

// ParsedSize converts a human size string ("4k", "1m", "10mib", "512", "1g")
// into a byte count.
func ParsedSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty size")
	}

	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "kib"), strings.HasSuffix(s, "k"):
		mult = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "kib"), "k")
	case strings.HasSuffix(s, "mib"), strings.HasSuffix(s, "m"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "mib"), "m")
	case strings.HasSuffix(s, "gib"), strings.HasSuffix(s, "g"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "gib"), "g")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}

	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must be non-negative: %d", n)
	}
	return n * mult, nil
}

// ParseSizes splits a comma-separated list of size strings.
func ParseSizes(list string) ([]int64, error) {
	parts := strings.Split(list, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := ParsedSize(p)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("no sizes provided")
	}
	return out, nil
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	switch c.Store.Provider {
	case objstore.ProviderCOS, objstore.ProviderS3:
	default:
		return fmt.Errorf("provider must be %q or %q", objstore.ProviderCOS, objstore.ProviderS3)
	}
	if c.Store.Bucket == "" {
		return errors.New("bucket is required (-bucket)")
	}
	if len(c.Sizes) == 0 {
		return errors.New("at least one size is required (-sizes)")
	}
	if c.Duration <= 0 {
		return errors.New("duration must be positive (-duration)")
	}
	if c.Concurrency <= 0 {
		return errors.New("concurrency must be positive (-concurrency)")
	}
	if c.ReadRatio < 0 || c.ReadRatio > 1 {
		return errors.New("read-ratio must be in [0,1]")
	}
	if c.Rate < 0 {
		return errors.New("rate must be non-negative (0 = unlimited)")
	}
	if c.Burst < 0 {
		return errors.New("burst must be non-negative")
	}
	return nil
}
