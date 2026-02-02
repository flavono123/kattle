#!/bin/bash
# Memory profiling comparison script for kattle
# Usage: compare-heap.sh [baseline_path] [current_path]
# Note: Run from project root where baseline_heap.pb.gz is located

set -e

BASELINE="${1:-baseline_heap.pb.gz}"
CURRENT="${2:-/tmp/current_heap.pb.gz}"

# Validate inputs
if [ ! -f "$BASELINE" ]; then
    echo "Error: Baseline file not found: $BASELINE"
    echo "Run from project root or specify full path"
    exit 1
fi

if [ ! -f "$CURRENT" ]; then
    echo "Error: Current heap profile not found: $CURRENT"
    echo "Capture a profile first using: curl -o $CURRENT http://localhost:6060/debug/pprof/heap"
    exit 1
fi

echo "Memory Profile Comparison"
echo "=========================="
echo "Baseline: $BASELINE ($(ls -lh "$BASELINE" | awk '{print $5}'))"
echo "Current:  $CURRENT ($(ls -lh "$CURRENT" | awk '{print $5}'))"
echo ""
echo "Differential Analysis (Delta):"
echo "------------------------------"

go tool pprof -diff_base="$BASELINE" -top "$CURRENT"
