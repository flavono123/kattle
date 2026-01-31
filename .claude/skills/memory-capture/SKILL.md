---
name: memory-capture
description: Memory profile snapshot capture and baseline comparison. Use for verification after memory optimization.
disable-model-invocation: false
user-invocable: true
allowed-tools: Bash, Read
---

# Memory Capture Skill

This skill provides tools for capturing memory profile snapshots and comparing them against baseline profiles to verify memory optimization efforts.

## Prerequisites

**IMPORTANT**: pprof server requires `KATTLE_DEBUG=1` environment variable:

```bash
# Start the application with debug mode enabled
KATTLE_DEBUG=1 wails dev
```

Verify pprof server is running:
```bash
curl -s http://localhost:6060/debug/pprof/ > /dev/null && echo "pprof OK" || echo "pprof NOT available"
```

**Note**: Run all commands from project root directory where `baseline_heap.pb.gz` and `baseline_goroutine.txt` are located.

## Capabilities

### 1. Heap Profile Capture

Captures the current heap memory profile:

```bash
curl -s -o /tmp/current_heap.pb.gz http://localhost:6060/debug/pprof/heap
```

This generates a compressed protobuf format heap profile that can be analyzed with `go tool pprof`.

### 2. Goroutine Profile Capture

Captures the current goroutine information:

```bash
curl -s http://localhost:6060/debug/pprof/goroutine?debug=1 > /tmp/current_goroutine.txt
```

This captures goroutine stack traces in text format for easy inspection.

### 3. Compare with Baseline

#### Heap Comparison

Compare current heap profile against baseline using pprof's diff mode:

```bash
go tool pprof -diff_base=baseline_heap.pb.gz -top /tmp/current_heap.pb.gz
```

This shows the differences in memory allocation between the baseline and current profiles.

#### Goroutine Count Comparison

Extract and compare actual goroutine counts (not line counts):

```bash
# Extract goroutine count from first line "goroutine N [...]"
head -1 baseline_goroutine.txt | grep -oE 'goroutine [0-9]+' | cut -d' ' -f2
head -1 /tmp/current_goroutine.txt | grep -oE 'goroutine [0-9]+' | cut -d' ' -f2
```

Or count total goroutines in the profile:
```bash
grep -c '^goroutine' baseline_goroutine.txt
grep -c '^goroutine' /tmp/current_goroutine.txt
```

### 4. Update Baseline (if improved)

When optimization is verified and improvements are confirmed:

```bash
cp /tmp/current_heap.pb.gz baseline_heap.pb.gz
cp /tmp/current_goroutine.txt baseline_goroutine.txt
```

This updates the baseline profiles for future comparisons.

## Usage

Use the provided `capture-profile.sh` script to capture both profiles in one command:

```bash
./scripts/capture-profile.sh [PORT]
```

Default port is 6060 if not specified.

## Workflow

1. Capture baseline profiles before optimization
2. Implement memory optimizations
3. Capture new profiles after optimization
4. Compare using the tools above
5. If improvements verified, update baseline
6. Continue iterating or celebrate success
