#!/bin/bash
# Kattle memory validation script for macOS
# Usage: ./quick-check.sh [--backend-max MB] [--webview-max MB] [--total-max MB]
#
# Validates memory usage of all kattle-related processes against thresholds.
# Exit codes: 0 = PASS, 1 = FAIL, 2 = ERROR

set -e

# Default thresholds (MB)
BACKEND_MAX=100
WEBVIEW_MAX=1024
TOTAL_MAX=2048

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --backend-max) BACKEND_MAX="$2"; shift 2 ;;
    --webview-max) WEBVIEW_MAX="$2"; shift 2 ;;
    --total-max) TOTAL_MAX="$2"; shift 2 ;;
    --json) OUTPUT_JSON=1; shift ;;
    -h|--help)
      echo "Usage: $0 [OPTIONS]"
      echo ""
      echo "Options:"
      echo "  --backend-max MB   Go backend threshold (default: 100)"
      echo "  --webview-max MB   WebView threshold (default: 1024)"
      echo "  --total-max MB     Total memory threshold (default: 2048)"
      echo "  --json             Output JSON format"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 2 ;;
  esac
done

# Find kattle main process
KATTLE_PID=$(pgrep -f "kattle.app/Contents/MacOS/kattle" | head -1)

if [ -z "$KATTLE_PID" ]; then
  echo "ERROR: Kattle process not found. Is wails dev running?"
  exit 2
fi

# Find associated WebKit processes by PID proximity
# Wails/WebKit spawns helper processes with PIDs close to the main app
# Typically within +/- 20 of the main process
PID_MIN=$((KATTLE_PID - 5))
PID_MAX=$((KATTLE_PID + 20))

collect_processes() {
  # Main kattle process
  rss_kb=$(ps -o rss= -p "$KATTLE_PID" 2>/dev/null || echo "0")
  rss_mb=$(echo "$rss_kb" | awk '{printf "%.1f", $1/1024}')
  echo "$KATTLE_PID|backend|Go Backend|$rss_mb"

  # WebKit processes in PID range
  ps -eo pid,rss,comm | while read pid rss comm; do
    # Skip header and non-numeric PIDs
    [[ "$pid" =~ ^[0-9]+$ ]] || continue

    # Check if in PID range and is WebKit
    if [ "$pid" -ge "$PID_MIN" ] && [ "$pid" -le "$PID_MAX" ] && [ "$pid" -ne "$KATTLE_PID" ]; then
      if echo "$comm" | grep -q "WebKit.WebContent"; then
        rss_mb=$(echo "$rss" | awk '{printf "%.1f", $1/1024}')
        echo "$pid|webview|WebView (Frontend)|$rss_mb"
      elif echo "$comm" | grep -q "WebKit.GPU"; then
        rss_mb=$(echo "$rss" | awk '{printf "%.1f", $1/1024}')
        echo "$pid|graphics|Graphics and Media|$rss_mb"
      elif echo "$comm" | grep -q "WebKit.Networking"; then
        rss_mb=$(echo "$rss" | awk '{printf "%.1f", $1/1024}')
        echo "$pid|networking|Networking|$rss_mb"
      fi
    fi
  done
}

# Collect process data
PROCS=$(collect_processes)

# Calculate totals by type
BACKEND_MB=0
WEBVIEW_MB=0
GRAPHICS_MB=0
NETWORKING_MB=0
TOTAL_MB=0

while IFS='|' read -r pid type name rss_mb; do
  [ -z "$pid" ] && continue
  case $type in
    backend) BACKEND_MB=$(echo "$BACKEND_MB + $rss_mb" | bc) ;;
    webview) WEBVIEW_MB=$(echo "$WEBVIEW_MB + $rss_mb" | bc) ;;
    graphics) GRAPHICS_MB=$(echo "$GRAPHICS_MB + $rss_mb" | bc) ;;
    networking) NETWORKING_MB=$(echo "$NETWORKING_MB + $rss_mb" | bc) ;;
  esac
  TOTAL_MB=$(echo "$TOTAL_MB + $rss_mb" | bc)
done <<< "$PROCS"

# Validation
BACKEND_PASS=$(echo "$BACKEND_MB <= $BACKEND_MAX" | bc)
WEBVIEW_PASS=$(echo "$WEBVIEW_MB <= $WEBVIEW_MAX" | bc)
TOTAL_PASS=$(echo "$TOTAL_MB <= $TOTAL_MAX" | bc)

ALL_PASS=1
[ "$BACKEND_PASS" -eq 0 ] && ALL_PASS=0
[ "$WEBVIEW_PASS" -eq 0 ] && ALL_PASS=0
[ "$TOTAL_PASS" -eq 0 ] && ALL_PASS=0

# Output
if [ -n "$OUTPUT_JSON" ]; then
  cat <<EOF
{
  "timestamp": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "verdict": $([ "$ALL_PASS" -eq 1 ] && echo '"PASS"' || echo '"FAIL"'),
  "processes": {
    "backend": {"memory_mb": $BACKEND_MB, "threshold_mb": $BACKEND_MAX, "pass": $([ "$BACKEND_PASS" -eq 1 ] && echo "true" || echo "false")},
    "webview": {"memory_mb": $WEBVIEW_MB, "threshold_mb": $WEBVIEW_MAX, "pass": $([ "$WEBVIEW_PASS" -eq 1 ] && echo "true" || echo "false")},
    "graphics": {"memory_mb": $GRAPHICS_MB},
    "networking": {"memory_mb": $NETWORKING_MB}
  },
  "total": {"memory_mb": $TOTAL_MB, "threshold_mb": $TOTAL_MAX, "pass": $([ "$TOTAL_PASS" -eq 1 ] && echo "true" || echo "false")}
}
EOF
else
  echo "=== Kattle Memory Validation ==="
  echo ""
  echo "Processes:"
  while IFS='|' read -r pid type name rss_mb; do
    [ -z "$pid" ] && continue
    printf "  %-25s %8s MB  (PID: %s)\n" "$name" "$rss_mb" "$pid"
  done <<< "$PROCS"
  echo ""
  echo "Summary:"
  printf "  %-20s %8s MB / %s MB  %s\n" "Go Backend" "$BACKEND_MB" "$BACKEND_MAX" "$([ "$BACKEND_PASS" -eq 1 ] && echo "PASS" || echo "FAIL")"
  printf "  %-20s %8s MB / %s MB  %s\n" "WebView" "$WEBVIEW_MB" "$WEBVIEW_MAX" "$([ "$WEBVIEW_PASS" -eq 1 ] && echo "PASS" || echo "FAIL")"
  printf "  %-20s %8s MB / %s MB  %s\n" "TOTAL" "$TOTAL_MB" "$TOTAL_MAX" "$([ "$TOTAL_PASS" -eq 1 ] && echo "PASS" || echo "FAIL")"
  echo ""
  if [ "$ALL_PASS" -eq 1 ]; then
    echo "VERDICT: PASS"
  else
    echo "VERDICT: FAIL"
  fi
fi

exit $([ "$ALL_PASS" -eq 1 ] && echo 0 || echo 1)
