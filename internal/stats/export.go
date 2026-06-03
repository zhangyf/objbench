package stats

import (
	"math/rand"
	"sort"
	"time"
)

// MaxSamplesPerOp caps how many latency samples a single agent reports per
// operation kind. Reservoir sampling keeps a uniform random subset so that
// merged percentiles across agents stay statistically faithful without
// shipping unbounded data through the coordination bucket.
const MaxSamplesPerOp = 10000

// AgentReport is the serializable result a single agent uploads for one size
// group. Latency samples are carried as nanosecond integers so the coordinator
// can merge them and recompute true global percentiles.
type AgentReport struct {
	AgentID string `json:"agentID"`
	SizeB   int64  `json:"sizeB"`

	WallNs int64 `json:"wallNs"`

	Upload   OpReport `json:"upload"`
	Download OpReport `json:"download"`
}

// OpReport holds per-operation aggregates plus a (possibly sampled) latency
// vector used for global percentile merging.
type OpReport struct {
	Count      int64 `json:"count"`
	Errors     int64 `json:"errors"`
	TotalBytes int64 `json:"totalBytes"`

	// SampleNs is a uniform random subset of successful-op latencies (ns).
	SampleNs []int64 `json:"sampleNs"`
	// Sampled is the number of successful ops the samples were drawn from
	// (>= len(SampleNs) when reservoir sampling kicked in). Used to weight
	// throughput/QPS correctly even though the sample vector is capped.
	Sampled int64 `json:"sampled"`
}

// BuildAgentReport converts raw samples into a serializable, sample-capped
// report for one size group.
func BuildAgentReport(agentID string, sizeB int64, samples []Sample, wall time.Duration) AgentReport {
	up := buildOp(samples, OpUpload)
	down := buildOp(samples, OpDownload)
	return AgentReport{
		AgentID:  agentID,
		SizeB:    sizeB,
		WallNs:   wall.Nanoseconds(),
		Upload:   up,
		Download: down,
	}
}

func buildOp(samples []Sample, kind OpKind) OpReport {
	var rep OpReport
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	reservoir := make([]int64, 0, MaxSamplesPerOp)
	var seen int64

	for _, s := range samples {
		if s.Kind != kind {
			continue
		}
		rep.Count++
		if s.Err {
			rep.Errors++
			continue
		}
		rep.TotalBytes += s.Bytes

		ns := s.Latency.Nanoseconds()
		seen++
		if len(reservoir) < MaxSamplesPerOp {
			reservoir = append(reservoir, ns)
		} else {
			// Reservoir sampling: replace with decreasing probability.
			j := rng.Int63n(seen)
			if j < int64(len(reservoir)) {
				reservoir[j] = ns
			}
		}
	}
	rep.SampleNs = reservoir
	rep.Sampled = seen
	return rep
}

// MergeReports aggregates per-agent reports for a single size group into a
// global Summary. QPS/throughput are summed across agents; percentiles are
// recomputed from the union of all agents' latency samples.
//
// wall is the coordinated test window (the planned duration); using the shared
// window keeps global QPS meaningful even if agents' local walls differ
// slightly. Pass 0 to fall back to the max per-agent wall.
func MergeReports(reports []AgentReport, wall time.Duration) Summary {
	if wall <= 0 {
		for _, r := range reports {
			if w := time.Duration(r.WallNs); w > wall {
				wall = w
			}
		}
	}

	upSamples := make([]int64, 0, 1<<16)
	downSamples := make([]int64, 0, 1<<16)
	var up, down LatencyStats

	for _, r := range reports {
		mergeOp(&up, &upSamples, r.Upload)
		mergeOp(&down, &downSamples, r.Download)
	}

	finalizeMerged(&up, upSamples, wall)
	finalizeMerged(&down, downSamples, wall)

	overall := up
	overall.Count += down.Count
	overall.Errors += down.Errors
	overall.TotalBytes += down.TotalBytes
	allSamples := append(append(make([]int64, 0, len(upSamples)+len(downSamples)), upSamples...), downSamples...)
	overall = LatencyStats{
		Count:      up.Count + down.Count,
		Errors:     up.Errors + down.Errors,
		TotalBytes: up.TotalBytes + down.TotalBytes,
	}
	finalizeMerged(&overall, allSamples, wall)

	return Summary{
		Wall:     wall,
		Upload:   up,
		Download: down,
		Overall:  overall,
	}
}

func mergeOp(dst *LatencyStats, samples *[]int64, op OpReport) {
	dst.Count += op.Count
	dst.Errors += op.Errors
	dst.TotalBytes += op.TotalBytes
	*samples = append(*samples, op.SampleNs...)
}

// finalizeMerged computes percentiles/min/max/mean from the merged sample
// vector and QPS/throughput from the aggregate counters over the shared wall.
func finalizeMerged(ls *LatencyStats, sampleNs []int64, wall time.Duration) {
	if len(sampleNs) > 0 {
		sort.Slice(sampleNs, func(i, j int) bool { return sampleNs[i] < sampleNs[j] })
		ls.Min = time.Duration(sampleNs[0])
		ls.Max = time.Duration(sampleNs[len(sampleNs)-1])

		var sum int64
		for _, v := range sampleNs {
			sum += v
		}
		ls.Mean = time.Duration(sum / int64(len(sampleNs)))
		ls.P50 = nsPercentile(sampleNs, 0.50)
		ls.P90 = nsPercentile(sampleNs, 0.90)
		ls.P95 = nsPercentile(sampleNs, 0.95)
		ls.P99 = nsPercentile(sampleNs, 0.99)
	}

	secs := wall.Seconds()
	if secs > 0 {
		success := ls.Count - ls.Errors
		ls.QPS = float64(success) / secs
		ls.ThroughputBps = float64(ls.TotalBytes) / secs
	}
}

func nsPercentile(sorted []int64, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return time.Duration(sorted[0])
	}
	if p >= 1 {
		return time.Duration(sorted[len(sorted)-1])
	}
	rank := int(p*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return time.Duration(sorted[rank-1])
}
