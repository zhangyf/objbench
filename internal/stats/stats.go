// Package stats collects latency samples and computes summary metrics.
package stats

import (
	"sort"
	"sync"
	"time"
)

// OpKind distinguishes upload from download operations.
type OpKind int

const (
	OpUpload OpKind = iota
	OpDownload
)

func (k OpKind) String() string {
	switch k {
	case OpUpload:
		return "upload"
	case OpDownload:
		return "download"
	default:
		return "unknown"
	}
}

// Sample is a single recorded operation result.
type Sample struct {
	Kind    OpKind
	Latency time.Duration
	Bytes   int64
	Err     bool
}

// Collector accumulates samples in a lock-protected slice.
type Collector struct {
	mu      sync.Mutex
	samples []Sample
}

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{samples: make([]Sample, 0, 4096)}
}

// Record appends a sample.
func (c *Collector) Record(s Sample) {
	c.mu.Lock()
	c.samples = append(c.samples, s)
	c.mu.Unlock()
}

// Snapshot returns a copy of the collected samples.
func (c *Collector) Snapshot() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sample, len(c.samples))
	copy(out, c.samples)
	return out
}

// LatencyStats holds aggregated latency metrics for a set of operations.
type LatencyStats struct {
	Count      int64
	Errors     int64
	TotalBytes int64

	Min  time.Duration
	Max  time.Duration
	Mean time.Duration
	P50  time.Duration
	P90  time.Duration
	P95  time.Duration
	P99  time.Duration

	// QPS is successful operations per second over the wall window.
	QPS float64
	// ThroughputBps is bytes per second over the wall window.
	ThroughputBps float64
}

// Summary aggregates upload, download, and combined metrics.
type Summary struct {
	Wall     time.Duration
	Upload   LatencyStats
	Download LatencyStats
	Overall  LatencyStats
}

// Compute builds a Summary from samples over the given wall-clock window.
func Compute(samples []Sample, wall time.Duration) Summary {
	var up, down, all []Sample
	for _, s := range samples {
		all = append(all, s)
		switch s.Kind {
		case OpUpload:
			up = append(up, s)
		case OpDownload:
			down = append(down, s)
		}
	}
	return Summary{
		Wall:     wall,
		Upload:   computeOne(up, wall),
		Download: computeOne(down, wall),
		Overall:  computeOne(all, wall),
	}
}

func computeOne(samples []Sample, wall time.Duration) LatencyStats {
	var ls LatencyStats
	if len(samples) == 0 {
		return ls
	}

	lats := make([]time.Duration, 0, len(samples))
	var sum time.Duration
	for _, s := range samples {
		ls.Count++
		if s.Err {
			ls.Errors++
			continue
		}
		ls.TotalBytes += s.Bytes
		lats = append(lats, s.Latency)
		sum += s.Latency
	}

	if len(lats) == 0 {
		return ls
	}

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	ls.Min = lats[0]
	ls.Max = lats[len(lats)-1]
	ls.Mean = sum / time.Duration(len(lats))
	ls.P50 = percentile(lats, 0.50)
	ls.P90 = percentile(lats, 0.90)
	ls.P95 = percentile(lats, 0.95)
	ls.P99 = percentile(lats, 0.99)

	secs := wall.Seconds()
	if secs > 0 {
		success := float64(len(lats))
		ls.QPS = success / secs
		ls.ThroughputBps = float64(ls.TotalBytes) / secs
	}
	return ls
}

// percentile returns the nearest-rank percentile from a sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank: ceil(p * N) - 1, clamped.
	rank := int(p*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
