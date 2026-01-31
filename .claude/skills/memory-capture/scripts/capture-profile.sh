#!/bin/bash
# Memory profile capture script for kattle
# Usage: capture-profile.sh [PORT]
# Requires: KATTLE_DEBUG=1 wails dev (or similar) to enable pprof server

set -e

PORT="${1:-6060}"

# Check if pprof server is available
if ! curl -s "http://localhost:$PORT/debug/pprof/" > /dev/null 2>&1; then
    echo "Error: pprof server not available at localhost:$PORT"
    echo "Ensure the app is running with KATTLE_DEBUG=1"
    exit 1
fi

curl -s -o /tmp/current_heap.pb.gz "http://localhost:$PORT/debug/pprof/heap"
curl -s "http://localhost:$PORT/debug/pprof/goroutine?debug=1" > /tmp/current_goroutine.txt

# Show goroutine count
GOROUTINE_COUNT=$(grep -c '^goroutine' /tmp/current_goroutine.txt || echo "0")
echo "Captured: /tmp/current_heap.pb.gz, /tmp/current_goroutine.txt (goroutines: $GOROUTINE_COUNT)"
