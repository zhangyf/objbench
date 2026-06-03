// Command objbench is an object-storage performance benchmark built on the
// github.com/zhangyf/objstore interface library. It measures upload/download
// latency across configurable object sizes, supports a fixed test duration and
// a read/write mix, and reports QPS, throughput, and P90/P95/P99 latencies.
//
// Modes:
//
//	objbench [flags]              single-machine benchmark (default)
//	objbench coordinate [flags]   publish a plan, collect & aggregate results
//	objbench agent [flags]        join a distributed run: register, wait, run,
//	                              upload results
//
// Distributed runs coordinate through an objstore bucket used as a bulletin
// board (ideally a small dedicated bucket, separate from the bucket under
// test). Total cluster load = per-agent quota × number of agents that join
// (A-mode: add a machine to add load, no plan change needed).
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	mode := "single"
	if len(args) > 0 {
		switch args[0] {
		case "agent":
			mode, args = "agent", args[1:]
		case "coordinate", "coord":
			mode, args = "coordinate", args[1:]
		case "single", "run":
			mode, args = "single", args[1:]
		case "-h", "--help", "help":
			usage()
			return
		}
	}

	var err error
	switch mode {
	case "single":
		err = runSingle(args)
	case "agent":
		err = runAgent(args)
	case "coordinate":
		err = runCoordinate(args)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "objbench: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `objbench — objstore performance benchmark

Usage:
  objbench [flags]              single-machine benchmark (default)
  objbench coordinate [flags]   publish a plan, then collect & aggregate results
  objbench agent [flags]        join a distributed run (register, wait, run, upload)

Run "objbench <mode> -h" for mode-specific flags.

Distributed model (A-mode):
  Total cluster load = per-agent quota × number of agents that join.
  To add load, start another agent — no plan change required.
  Coordination goes through a bucket bulletin board; use a small DEDICATED
  bucket (separate from the bucket under test) via the coordinate/agent
  -coord-* flags.
`)
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
