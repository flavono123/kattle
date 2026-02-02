---
name: go-quality
description: Go code quality rules and standards for kattle project. Apply when writing or reviewing Go code.
disable-model-invocation: true
user-invocable: false
allowed-tools: Read, Edit, Write, Grep, Glob
---

# Go Code Quality Rules

These rules apply to all Go code in the kattle project. Use this skill when writing, reviewing, or refactoring Go code.

## 1. Error Handling

**Always check and handle errors explicitly.** Never ignore errors with `_`.

```go
// ❌ BAD
data, _ := os.ReadFile(path)

// ✅ GOOD
data, err := os.ReadFile(path)
if err != nil {
    return fmt.Errorf("failed to read file %s: %w", path, err)
}
```

**Use `%w` for error wrapping** (Go 1.13+). This allows error chain inspection with `errors.Is()` and `errors.As()`.

```go
// ❌ BAD
return fmt.Errorf("failed to create client: %v", err)

// ✅ GOOD
return fmt.Errorf("failed to create client: %w", err)
```

Never panic in library code. Only panic in `main()` or `init()` functions.

## 2. Exported vs Unexported Identifiers

**Only export what's needed** by external packages. Check usage before exporting:

- Used only within `internal/` package? Use lowercase (unexported)
- Used by `cmd/` or external consumers? Use uppercase (exported)

```go
// ❌ BAD: Unnecessarily exported
func InternalHelper() {}

// ✅ GOOD: Unexported when internal-only
func internalHelper() {}

// ✅ GOOD: Exported for external use
func ListContexts() ([]string, error) {}
```

## 3. Resource Management

**Always use `defer` for cleanup.** Lock/unlock, file closing, and resource release must use defer.

```go
// ❌ BAD
mu.Lock()
doWork()
mu.Unlock()

// ✅ GOOD
mu.Lock()
defer mu.Unlock()
doWork()
```

```go
// ✅ GOOD: Close resources with defer
f, err := os.Open(path)
if err != nil {
    return err
}
defer f.Close()
```

## 4. Concurrency Safety

**Use `sync.RWMutex` for read-heavy workloads.** Acquire read locks for queries, write locks for mutations.

```go
// ✅ GOOD: Read lock for reads
clientSetsMu.RLock()
cs, exists := clientSets[contextName]
clientSetsMu.RUnlock()

// ✅ GOOD: Write lock for writes
clientSetsMu.Lock()
defer clientSetsMu.Unlock()
clientSets[contextName] = cs
```

**Use double-check pattern for cached values** to prevent race conditions:

```go
// ✅ GOOD: Check without lock, then recheck after acquiring lock
mu.RLock()
if val, exists := cache[key]; exists {
    mu.RUnlock()
    return val, nil
}
mu.RUnlock()

mu.Lock()
defer mu.Unlock()

// Double-check after acquiring write lock
if val, exists := cache[key]; exists {
    return val, nil
}

cache[key] = newValue
return newValue, nil
```

**Prevent goroutine leaks:** Use context for cancellation, never fire-and-forget goroutines.

```go
// ✅ GOOD: Goroutine respects context cancellation
go func(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        default:
            doWork()
        }
    }
}(ctx)
```

## 5. Context Handling

**Pass `context.Context` as the first parameter** in functions that perform I/O or network operations.

```go
// ✅ GOOD: Accept context parameter
func FetchData(ctx context.Context, url string) error {
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    // ...
}
```

**Pass context down the call stack.** Never create a new `context.Background()` in the middle of execution.

```go
// ❌ BAD: Creating new context
func handler() {
    ctx := context.Background()
    fetchData(ctx)
}

// ✅ GOOD: Using passed context
func handler(ctx context.Context) {
    fetchData(ctx)
}
```

## 6. Nil Checks

**Always check for nil before dereferencing** pointers or accessing map values.

```go
// ❌ BAD: Potential nil dereference
result := config.Clusters[name].Server

// ✅ GOOD: Explicit nil check
cluster, exists := config.Clusters[name]
if !exists || cluster == nil {
    return fmt.Errorf("cluster %s not found", name)
}
result := cluster.Server
```

## 7. Testing

**Write table-driven tests** for complex logic. Structure tests with clear test cases, inputs, and expected outputs.

```go
func TestIndexesToRanges(t *testing.T) {
    tests := []struct {
        name     string
        input    []int
        expected [][2]int
        wantErr  bool
    }{
        {"empty", []int{}, [][2]int{}, false},
        {"single", []int{0}, [][2]int{{0, 0}}, false},
        {"consecutive", []int{0, 1, 2}, [][2]int{{0, 2}}, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := indexesToRanges(tt.input)
            if (result == nil) != tt.wantErr {
                t.Errorf("got %v, want error=%v", result, tt.wantErr)
            }
            // Assert result == tt.expected
        })
    }
}
```

## Quick Checklist

Before committing Go code, verify:

- [ ] All errors are checked and wrapped with `%w`
- [ ] No unused exported functions (CapitalCase)
- [ ] `defer` used for all cleanup operations (locks, files, resources)
- [ ] Mutex locks have corresponding `defer` unlock
- [ ] No race conditions in concurrent code (no unprotected shared state)
- [ ] Nil checks before pointer dereference
- [ ] `context.Context` passed down call stack (not created in middle of execution)
- [ ] No goroutine leaks (goroutines respect context cancellation)
- [ ] Table-driven tests for complex logic
- [ ] Code passes `gofmt`, `golint`, `go vet`, and `go test`

## When to Apply This Skill

Apply this skill when:
- Writing new Go code for kattle
- Reviewing Go code changes
- Refactoring existing Go functions
- Creating or updating Go tests
- Fixing bugs in Go code
