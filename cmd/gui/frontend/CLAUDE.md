# Frontend (cmd/gui/frontend)

React app for the Kattle desktop GUI, running inside a Wails WebView.

## Stack

- React 18, TypeScript 5.9, Vite
- TanStack Table v8 + TanStack Virtual v3 (virtualized data tables)
- Radix UI (dialog, dropdown, checkbox, tooltip, etc.)
- Tailwind CSS + tailwindcss-animate
- dnd-kit (drag-drop column reordering)
- fuzzysort + @tanstack/match-sorter-utils (search/ranking)

## Directory Structure

```
src/
  components/
    DIYTable.tsx            # Core table: dual mode, virtualized, drag-drop columns
    DynamicFieldTree.tsx    # K8s OpenAPI schema tree for field selection
    MainView.tsx            # Top-level: GVK/context switching, mode detection
    SearchBar.tsx           # Global filter input with debounce
    FavoritesPopover.tsx    # Saved GVK favorites
  hooks/
    useResourceData.ts      # Pull model: collect keys -> batch fetch -> state
    useWindowedData.ts      # Windowed mode: server-side sort/filter/pagination
    useTree.ts              # Field tree state (selected fields, preview)
    useFavorites.ts         # Favorites persistence
  lib/
    columns.ts              # Dynamic TanStack column definitions from selected fields
    utils.ts                # Shared utilities
  wailsjs/                  # Auto-generated Wails bindings (DO NOT EDIT)
    go/main/App.{js,d.ts}   #   Go method bindings
    runtime/runtime.{js,d.ts} # EventsOn, EventsEmit, etc.
```

## Key Components

### DIYTable

The central component - a fully customizable data table.

- **Dual mode**: Regular (all data in memory) vs Windowed (server-side sort/filter/pagination)
- **Windowed mode** activates when `IsWindowedModeEnabled()` returns true (`KATTLE_USE_SQLSTORE=1`)
- **TanStack Table** with `manualSorting`, `manualFiltering`, `manualPagination` in windowed mode
- **TanStack Virtual** for row virtualization (handles 12K+ rows, only renders visible ~50)
- **dnd-kit** for drag-drop column reorder; columns are resizable via header drag
- **Cell flash animation**: Cells flash on real-time updates from informer watch events

### DynamicFieldTree

Renders K8s resource schema as a collapsible tree. Users drag fields to the table.

- Built from OpenAPI schema via Go backend's field discovery
- Supports wildcard (`*`) node for bulk selection of all child fields
- Preview column on hover (temporary column shown without full selection)
- Persists field selection per GVK

### useWindowedData Hook

Server-side data management for large datasets.

- Tracks visible range from virtualizer's `onChange` callback
- Fetches data via `GetResourcesRangeFiltered(JSON.stringify(queryParams))`
- Converts TanStack Table sorting/filter state into `QueryParams` for SQLite
- Handles async field extraction: shows skeleton during `extractingFields` state
- Listens for `fields:ready` event to re-fetch after background extraction completes
- Uses `lastFetchRangeRef` to avoid redundant fetches on same range

### useResourceData Hook

Pull model data management (used when windowed mode is off).

- Subscribes to `resource:update` events (receives keys only, ~50B each)
- Batches pending keys in a Set, debounces 100ms
- Calls `GetResourcesByKeys(keys)` to pull full data from Go/SQLite
- Merges results into React state array

## Wails IPC

**Go function calls** (auto-generated bindings):
```typescript
import { GetResourcesRange } from '../wailsjs/go/main/App';
const rows = await GetResourcesRange(0, 50, 'metadata.name', false);
```

**Event subscription**:
```typescript
import { EventsOn } from '../wailsjs/runtime/runtime';
const cleanup = EventsOn('resource:update', ({ type, key }) => { ... });
```

**Bindings are auto-generated** when `wails dev` is running. Do not run `wails generate module` concurrently with `wails dev`. Bindings live in `src/wailsjs/` - never edit these files.

## Events from Backend

| Event | Payload | When |
|-------|---------|------|
| `resource:update` | `{type: "ADDED"|"MODIFIED"|"DELETED", key: string}` | Informer watch event |
| `watch:status` | `{status: "syncing"|"ready", count: number}` | Watch lifecycle |
| `fields:ready` | (none) | Async field extraction complete |

## Testing

```bash
npm run test:run    # Vitest + React Testing Library (single run)
npm run test        # Watch mode
```

## Code Quality

See `CODE_QUALITY.md` for full guidelines. Key rules:

- **No type assertions** (`as`) - use type guards or generics
- **No unused imports**
- **Safe array access** - always check for undefined before accessing
- **useCallback** for event handlers passed as props
- **Complete dependency arrays** in useEffect/useMemo/useCallback
