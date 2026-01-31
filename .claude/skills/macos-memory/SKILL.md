---
name: macos-memory
description: macOS native memory analysis tools (leaks, heap, vmmap, Instruments). Use for native app memory debugging.
disable-model-invocation: true
user-invocable: true
allowed-tools: Bash, Read
---

# macOS Memory Analysis Skill

Comprehensive guide for analyzing memory usage and detecting leaks in native macOS applications using Apple's native profiling tools.

## 1. leaks - Memory Leak Detection

The `leaks` tool detects unreachable memory allocations that have been leaked.

### Basic Usage

```bash
leaks <pid>
```

Check a running process for memory leaks:
```bash
leaks 1234
```

### Run App and Check at Exit

Run an application and automatically check for leaks when it exits:
```bash
leaks --atExit -- ./myapp
leaks --atExit -- /path/to/binary arg1 arg2
```

### Common Options

- `leaks <pid>` - Check running process
- `leaks --atExit -- <command>` - Check application at exit
- `leaks --nocontext` - Suppress stack trace context
- `leaks --nostacks` - Don't print stack traces (faster)

### Example Output

The tool will report:
- Number of leaked memory blocks
- Total leaked memory size
- Stack traces showing where leaks originated

## 2. heap - Heap Analysis

The `heap` tool provides detailed information about heap memory allocations.

### Basic Usage

```bash
heap <pid>
```

Show heap summary:
```bash
heap 1234
```

### Show All Allocations

```bash
heap <pid> -addresses all
```

Dump all allocation addresses (detailed, slow for large heaps):
```bash
heap 1234 -addresses all
```

### Common Options

- `heap <pid>` - Show heap summary
- `heap <pid> -addresses all` - Show all individual allocations
- `heap <pid> -addresses <size>` - Show allocations above a certain size
- `heap <pid> -diffFrom <file>` - Compare heap to a previous snapshot

### Interpretation

- **Zone** - Memory zone information
- **Count** - Number of allocations
- **Total** - Total allocated bytes
- **Avg** - Average allocation size
- **Max** - Largest single allocation

## 3. vmmap - Virtual Memory Map

The `vmmap` tool shows the complete virtual memory map of a process, including all memory regions and their usage.

### Basic Usage

```bash
vmmap <pid>
```

Show full virtual memory map:
```bash
vmmap 1234
```

### Filter for Malloc Regions

```bash
vmmap <pid> | grep MALLOC
```

### Memory Region Types

Common memory region types in output:
- **MALLOC** - Memory allocated by malloc
- **STACK** - Thread stacks
- **TEXT** - Code/executable sections
- **DATA** - Static data
- **PAGEZERO** - Zero page (guard region)
- **OBJC** - Objective-C runtime
- **CF** - CoreFoundation

### Example Analysis

```bash
vmmap 1234 | grep -E "MALLOC|TOTAL"
```

Shows only malloc regions and totals for quick overview.

## 4. Instruments CLI - Advanced Profiling

The `xcrun instruments` command-line tool provides professional profiling capabilities.

### Leaks Instrument

Detect memory leaks while capturing detailed information:
```bash
xcrun instruments -t "Leaks" ./myapp
xcrun instruments -t "Leaks" -l 30000 ./myapp
```

### Allocations Instrument

Track all memory allocations:
```bash
xcrun instruments -t "Allocations" ./myapp
xcrun instruments -t "Allocations" -l 30000 ./myapp
```

### Common Options

- `-t "Name"` - Instrument template to use
- `-l <duration>` - Recording duration in milliseconds
- `-o <path>` - Output file path (.trace format)
- `-s <delay>` - Start delay in milliseconds
- `-p <PID>` - Attach to running process

### Available Instruments

Common instruments for memory analysis:
- **Leaks** - Memory leak detection
- **Allocations** - Memory allocation tracking
- **VM Tracker** - Virtual memory analysis
- **System Trace** - Low-level system events

### Saving Results

```bash
xcrun instruments -t "Leaks" -o /tmp/myapp.trace ./myapp
```

Results can be opened in Instruments.app for GUI analysis.

## 5. Finding Process ID (PID)

### Using pgrep

Find process by partial name match:
```bash
pgrep -f kattle
pgrep -f "kattle.*--flag"
```

Get first matching PID:
```bash
pgrep -f kattle | head -1
```

### Using ps

Alternative method with ps:
```bash
ps aux | grep kattle
ps aux | grep -i "MyApp"
```

Extract PID from ps output:
```bash
ps aux | grep kattle | grep -v grep | awk '{print $2}'
```

### Using Activity Monitor CLI

```bash
open -a Activity\ Monitor
```

## Workflow Examples

### Quick Memory Check

```bash
PID=$(pgrep -f kattle | head -1)
echo "Process: kattle (PID: $PID)"
ps -o rss= -p $PID | awk '{printf "RSS: %.2f MB\n", $1/1024}'
```

### Comprehensive Leak Detection

```bash
PID=$(pgrep -f myapp | head -1)
echo "=== Leak Detection ==="
leaks $PID

echo "=== Heap Summary ==="
heap $PID

echo "=== Top Malloc Regions ==="
vmmap $PID | grep -i malloc | head -10
```

### Profile Until Problem Occurs

```bash
# Start profiling with Leaks instrument
xcrun instruments -t "Leaks" -o /tmp/profile.trace ./myapp

# Open results in Instruments.app
open /tmp/profile.trace
```

### Memory Growth Analysis

```bash
PID=$(pgrep -f myapp | head -1)

# Capture initial heap snapshot
heap $PID > /tmp/heap_initial.txt

# Wait for activity
sleep 10

# Compare with second snapshot
heap $PID -diffFrom /tmp/heap_initial.txt
```

## Tips and Best Practices

1. **Run as appropriate user** - May need `sudo` for privileged processes
2. **Stop background activity** - Close other apps to reduce noise
3. **Warm up the app** - Run app briefly to reach stable state
4. **Use vmmap for overview** - Get big picture of memory usage
5. **Use heap for detail** - Drill down into allocation patterns
6. **Use leaks for confirmation** - Verify suspected leaks
7. **Capture baselines** - Take snapshots at known good states
8. **Compare snapshots** - Use diff features to find growth

## Common Issues

### Tool Not Found

If tools aren't in PATH, use full paths:
```bash
/usr/bin/leaks <pid>
/usr/bin/heap <pid>
/usr/bin/vmmap <pid>
```

### Permission Denied

Need appropriate privileges for some processes:
```bash
sudo leaks <pid>
sudo xcrun instruments -t "Leaks" ./app
```

### Instruments Framework Issues

Ensure Xcode command line tools are installed:
```bash
xcode-select --install
```

## Related Commands

- **sample** - CPU profiling sampler
- **dtrace** - Dynamic tracing
- **malloc_history** - Track malloc allocations
- **memory_statistics** - System-wide memory stats
