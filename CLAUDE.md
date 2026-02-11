# Kattle

Kubernetes resource explorer - a macOS desktop app for fast, flexible browsing of large-scale clusters.

## Tech Stack

- **Backend**: Go 1.25 + Wails v2 (macOS WebView bridge)
- **Frontend**: React 18 + TypeScript + Vite
- **K8s**: client-go informer (watch/list)
- **Storage**: SQLite (mattn/go-sqlite3, requires CGO)
- **UI**: TanStack Table + TanStack Virtual, Radix UI, Tailwind CSS, dnd-kit
- **Test**: Ginkgo/Gomega (Go), Vitest (Frontend)

## Directory Structure

```
cmd/gui/                    # Desktop GUI (Wails) - PRIMARY TARGET
  app.go                    #   Wails bindings, watch lifecycle, SQLStore integration
  frontend/                 #   React app (Vite) - has its own CLAUDE.md
    src/
      components/
        DIYTable.tsx        #     Core table: virtualized, drag-drop columns, dual mode
        DynamicFieldTree.tsx #     K8s schema tree with field selection
        MainView.tsx        #     GVK/context switching, mode detection
      hooks/
        useResourceData.ts  #     Pull model: event -> batch fetch -> React state
        useWindowedData.ts  #     Windowed mode: server-side sort/filter/pagination
        useTree.ts          #     Field tree state management
      lib/
        columns.ts          #     Dynamic column generation from selected fields

internal/kube/              # Kubernetes client layer
  client.go                 #   Informer setup, watch/list orchestration
  sqlstore.go               #   SQLite storage (WAL, connection pool, UPSERT)
  sqlstore_test.go          #   SQLite tests (Ginkgo)
  keyonlystore.go           #   Memory-efficient informer cache.Store (keys only)
  fieldstore.go             #   In-memory field extraction (legacy, being replaced by SQLStore)
  field.go                  #   OpenAPI schema -> field tree conversion
  resource.go               #   K8s resource type definitions
  schema.go                 #   GVR/GVK discovery

cmd/kupid/                  # CLI TUI (Bubble Tea) - DEPRECATED, maintenance only
internal/ui/                # TUI components - DEPRECATED
website/                    # Docs site (Astro)
planning/                   # Feature spec documents
```

## Architecture

### Design Goals

1. **Real-time updates**: Informer watch events reflected in UI instantly (cell flash animation)
2. **DIY composition**: Users select fields from OpenAPI schema tree to build custom table layouts
3. **Fast responsiveness**: Instant scroll, sort, and search even with 12K+ resources

### Data Flow: Pull Model

To avoid Wails `eval()` memory accumulation, we push **keys only** and let the frontend pull data on demand.

```
K8s API Server
    | watch stream
    v
Informer (client-go)
    |
    |-> KeyOnlyStore        Holds keys only (~5MB for 12K), satisfies informer cache.Store
    |
    +-> Event Handler
          |
          |-> SQLite UPSERT    Stores full JSON + extracted fields (off-heap, OS mmap managed)
          |
          +-> EventsEmit       Sends {type, key} ~50B to WebView (vs ~50KB/event before)
                    |
                    v
              React (WebView)
                    |
                    |- useResourceData    Collects keys -> debounce 100ms -> GetResourcesByKeys()
                    |                     Keeps all data in React state (small datasets)
                    |
                    +- useWindowedData    Keeps only ~50 visible rows, server-side sort/filter
                                          GetResourcesRange() + GetResourceCountFiltered()
```

### Windowed Mode (`KATTLE_USE_SQLSTORE=1`)

Keeps WebView memory at ~5MB for large datasets (1K+ resources).

- **Server-side processing**: Sort, filter, pagination handled in SQLite
- **TanStack Table**: `manualSorting`, `manualFiltering`, `manualPagination` enabled
- **Virtualizer**: `count=totalCount` for full scrollbar, actual data fetched only for visible range
- **Async field extraction**: On cache miss, goroutine extracts fields in background -> emits `fields:ready` event

### DIY Table (DIYTable.tsx)

Core component allowing users to build custom table layouts.

- **Column source**: Drag fields from DynamicFieldTree (K8s OpenAPI schema based)
- **Dual mode**: Regular (all-in-memory) / Windowed (server-side) - auto-switches
- **Features**: Column resize, drag reorder, header click sort, global search, cell flash animation
- **Virtualization**: TanStack Virtual for thousands of rows with overscan

### Store Architecture

| Store | Location | Role | Memory |
|-------|----------|------|--------|
| KeyOnlyStore | Go heap | Informer cache.Store interface | ~5MB (keys only) |
| SQLStore | SQLite (mmap) | Full resource JSON + extracted fields | OS managed |
| FieldStore | Go heap | In-memory field extraction (legacy) | ~60MB |
| React state | WebView | Display data | ~5MB (windowed) |

## Key Wails Bindings (cmd/gui/app.go)

| Method | Purpose |
|--------|---------|
| `StartWatch(gvk, contexts, fields)` | Start informer watch |
| `StopWatch()` | Stop and clean up watch |
| `SetSelectedFields(fields)` | Update field selection (cache hit -> immediate / miss -> async + `fields:ready`) |
| `GetResourcesRange(start, end, sort, desc)` | Range query (windowed mode) |
| `GetResourcesRangeFiltered(paramsJSON)` | Filtered + sorted + paginated query |
| `GetResourceCount()` / `GetResourceCountFiltered(paramsJSON)` | Total / filtered count |
| `GetResourcesByKeys(keys)` | Key-based lookup (pull model) |
| `IsWindowedModeEnabled()` | Check if SQLStore mode is active |

## Event System

| Event | Direction | Payload | Purpose |
|-------|-----------|---------|---------|
| `resource:update` | Go -> WebView | `{type, key}` ~50B | Resource change notification |
| `watch:status` | Go -> WebView | `{status, count}` | Watch state (syncing/ready) |
| `fields:ready` | Go -> WebView | (none) | Async field extraction complete |

## Feature Flags

```bash
KATTLE_USE_SQLSTORE=1    # Enable SQLStore + windowed mode
KATTLE_DEBUG=1           # Enable pprof endpoint (localhost:6060)
```

## Commands

```bash
# Development
wails dev                                       # GUI dev mode (hot reload)
KATTLE_USE_SQLSTORE=1 wails dev                 # With windowed mode

# Test
go test -C /Users/hansuk.hong/P/kattle ./...    # Go tests
npm --prefix cmd/gui/frontend test:run           # Frontend tests

# Memory Profiling (requires KATTLE_DEBUG=1)
curl -o heap.pb.gz http://localhost:6060/debug/pprof/heap
go tool pprof -top heap.pb.gz
```

## Code Quality

- **Go**: `CODE_QUALITY_GO.md` - error wrapping (`%w`), sync.RWMutex, table-driven tests
- **Frontend**: `cmd/gui/frontend/CODE_QUALITY.md` - no `as` assertions, safe array access, complete hook deps

## Specs

- `planning/260201-sqlite-pull-model.spec.md` - SQLite Pull Model design
- `planning/260201-virtualized-table-lazy-loading.spec.md` - Virtualized table + lazy loading
