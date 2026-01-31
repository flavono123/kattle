---
name: memory-profiling
description: Go application memory profiling and analysis. Use when investigating memory leaks or high memory usage.
disable-model-invocation: true
user-invocable: true
allowed-tools: Bash, Read, Grep
---

# Memory Profiling Skill

Investigate and analyze Go application memory usage through heap profiling and comparative analysis.

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

## Current State Check

Before profiling, establish the current memory baseline:

```bash
ps aux | grep -E "(PID|kattle|gui)" | head -20
```

Verify baseline profile exists (from project root):
```bash
ls -lh baseline_heap.pb.gz 2>/dev/null || echo "No baseline found"
```

## Heap Profile Collection

Collect a heap profile from the running application:

```bash
curl -o /tmp/heap.pb.gz http://localhost:6060/debug/pprof/heap
```

Verify collection was successful:
```bash
ls -lh /tmp/heap.pb.gz
```

**Alternative**: The app provides `DumpMemory(label)` function which saves labeled dumps to `~/kattle-dumps/`:
- Format: `heap_<timestamp>_<seq>_<label>.pb.gz`
- Location: `~/kattle-dumps/`

## Analysis Commands

### Top 10 Memory Allocators

Get the most significant memory allocators:

```bash
go tool pprof -top /tmp/heap.pb.gz | head -15
```

### Differential Analysis (vs Baseline)

Compare current heap profile against a known baseline:

```bash
go tool pprof -diff_base=baseline_heap.pb.gz -top /tmp/heap.pb.gz
```

Or use the helper script:
```bash
bash /Users/hansuk.hong/P/kattle/.claude/skills/memory-profiling/scripts/compare-heap.sh [baseline_path] [current_path]
```

### Interactive Investigation

For deeper analysis, use interactive mode:

```bash
go tool pprof /tmp/heap.pb.gz
# Then use commands like: top, list <function>, alloc_space, alloc_objects
```

## Reporting Format

When analyzing memory profiles, report:

1. **Top 10 Allocators**: List of functions responsible for largest allocations
2. **Delta from Baseline**: Increase/decrease in allocation patterns since baseline
3. **Suspect Areas**: Functions or packages with unexplained growth
4. **Memory Metrics**:
   - Total inuse_space (bytes)
   - Total alloc_objects (count)
   - Per-function percentages of total heap

### Example Report Structure

```
Memory Profile Analysis Report
==============================

Top 10 Allocators (Current):
1. package.function - X.XGB (XX%)
2. package.function - X.XGB (XX%)
...

Changes vs Baseline:
- package.function: +X.XGB (new allocation)
- package.function: -X.XGB (reduction)

Suspect Areas:
- package.function: Excessive allocation growth
- Recommendations for investigation
```

## Usage Workflow

1. Establish baseline: Collect heap profile when application is in healthy state
2. Run application under load or over time
3. Collect current heap profile
4. Compare using differential analysis
5. Investigate top allocators and changes
6. Identify memory leaks or optimization opportunities
