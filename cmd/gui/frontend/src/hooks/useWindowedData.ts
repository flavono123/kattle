import { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import {
  StartWatch,
  StopWatch,
  SetSelectedFields,
  GetResourceCount,
  GetResourcesRange,
  GetResourceCountFiltered,
  GetResourcesRangeFiltered,
} from '../../wailsjs/go/main/App';
import type { main } from '../../wailsjs/go/models';
import { getResourceKey, diffFields, type CellChange } from '../lib/resource-utils';

export type WatchStatus = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface SortConfig {
  field: string;      // e.g., "metadata.creationTimestamp"
  descending: boolean;
}

// Filter operation types (matches Go FilterOp)
export type FilterOp = 'contains' | 'equals' | 'startsWith' | 'gt' | 'lt' | 'gte' | 'lte' | 'in';

// Single filter condition (matches Go Filter)
export interface Filter {
  field: string;  // JSON path: "metadata.namespace"
  op: FilterOp;
  value: string | number | string[];
}

// Query parameters for filtered API (matches Go QueryParams)
export interface QueryParams {
  start: number;
  end: number;
  sortField: string;
  sortDesc: boolean;
  filters: Filter[];
  search: string;
}

export interface UseWindowedDataOptions {
  /** Batch interval in ms for range fetches (default: 100) */
  fetchInterval?: number;
  /** Number of rows to fetch beyond visible range (default: 20) */
  overscan?: number;
  /** Selected field paths to extract */
  selectedFields?: string[];
  /** Sort configuration (server-side) */
  sort?: SortConfig;
  /** Global search term (server-side filtering) */
  search?: string;
  /** Column filters (server-side filtering) */
  filters?: Filter[];
  /** Preview field path (hover preview, debounced 200ms) */
  previewField?: string;
}

export interface UseWindowedDataResult {
  /** Total count of resources (for virtualizer) */
  totalCount: number;
  /** Currently loaded rows (sparse - only visible range) */
  visibleRows: Map<number, Record<string, unknown>>;
  /** Loading state */
  loading: boolean;
  /** Whether a filter/search change is pending (count not yet refreshed) */
  filterPending: boolean;
  /** Error from fetch or watch */
  error: Error | null;
  /** Watch connection status */
  watchStatus: WatchStatus;
  /** Whether initial sync has completed (first sync:complete received) */
  initialSyncComplete: boolean;
  /** Get row data by virtual index (returns undefined if not loaded) */
  getRowData: (index: number) => Record<string, unknown> | undefined;
  /** Get stable row ID */
  getRowId: (row: Record<string, unknown>) => string;
  /** Notify when visible range changes */
  onVisibleRangeChange: (startIndex: number, endIndex: number) => void;
  /** Current fetch range (for debugging) */
  fetchRange: { start: number; end: number };
  /** Whether async field extraction is in progress (cache miss) */
  extractingFields: boolean;
  /** Cells that changed in the most recent fetch (for flash animation) */
  changedCells: CellChange[];
  /** Manual refresh */
  refresh: () => void;
}

/**
 * Hook for virtualized data loading - only fetches visible rows
 *
 * Key differences from useResourceData:
 * - Does NOT load all data into memory
 * - Only fetches rows in visible range + overscan
 * - Uses server-side sorting via SQLite
 * - Significantly reduces WebView memory usage
 */
export function useWindowedData(
  gvk: main.MultiClusterGVK | null,
  contexts: string[],
  options: UseWindowedDataOptions = {}
): UseWindowedDataResult {
  const {
    fetchInterval = 100,
    overscan = 20,
    selectedFields = [],
    sort,
    search = '',
    filters = [],
    previewField,
  } = options;

  const selectedFieldsKey = JSON.stringify(selectedFields);
  const sortKey = sort ? `${sort.field}:${sort.descending}` : '';
  const filtersKey = JSON.stringify(filters);

  // Debounced search to avoid excessive API calls
  const [debouncedSearch, setDebouncedSearch] = useState(search);
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(search);
    }, 200);  // 200ms debounce
    return () => clearTimeout(timer);
  }, [search]);

  const searchKey = debouncedSearch;

  // Check if we have any filters/search (use filtered API)
  const hasFilters = debouncedSearch.length > 0 || filters.length > 0;

  // State
  const [totalCount, setTotalCount] = useState(0);
  const totalCountRef = useRef(0);  // Ref avoids stale closure in fetchRange_
  const [visibleRows, setVisibleRows] = useState<Map<number, Record<string, unknown>>>(new Map());
  const [loading, setLoading] = useState(false);
  const [filterPending, setFilterPending] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [watchStatus, setWatchStatus] = useState<WatchStatus>('disconnected');
  const [initialSyncComplete, setInitialSyncComplete] = useState(false);
  const [extractingFields, setExtractingFields] = useState(false);
  const [changedCells, setChangedCells] = useState<CellChange[]>([]);

  // Debounced preview field (200ms delay for hover)
  const [debouncedPreview, setDebouncedPreview] = useState<string | undefined>();
  useEffect(() => {
    if (!previewField) {
      setDebouncedPreview(undefined);
      return;
    }
    const timer = setTimeout(() => setDebouncedPreview(previewField), 200);
    return () => clearTimeout(timer);
  }, [previewField]);

  // Refs for mutable state
  const watchGenRef = useRef(0);
  const visibleRangeRef = useRef({ start: 0, end: 50 });
  const pendingFetchRef = useRef(false);
  const lastFetchRangeRef = useRef({ start: -1, end: -1 });
  const fetchTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Stable ref to fetchRange_ — updated each render, used in effects to avoid stale closures
  const fetchRangeRef = useRef<() => Promise<void>>(() => Promise.resolve());

  // Computed fetch range (visible + overscan)
  const fetchRange = useMemo(() => ({
    start: Math.max(0, visibleRangeRef.current.start - overscan),
    end: visibleRangeRef.current.end + overscan,
  }), [visibleRangeRef.current.start, visibleRangeRef.current.end, overscan]);

  // Ref for stable access to current params without causing re-renders
  const paramsRef = useRef({
    hasFilters,
    sort,
    debouncedSearch,
    filters,
  });
  paramsRef.current = { hasFilters, sort, debouncedSearch, filters };

  // Fetch data for current range
  const fetchRange_ = useCallback(async () => {
    if (!gvk || contexts.length === 0) return;

    const currentWatchGen = watchGenRef.current;
    const params = paramsRef.current;
    const range = {
      start: Math.max(0, visibleRangeRef.current.start - overscan),
      end: Math.min(totalCountRef.current, visibleRangeRef.current.end + overscan),
    };

    // Skip if range hasn't changed significantly
    if (
      range.start === lastFetchRangeRef.current.start &&
      range.end === lastFetchRangeRef.current.end
    ) {
      return;
    }

    lastFetchRangeRef.current = range;
    pendingFetchRef.current = true;

    try {
      let rows: Record<string, unknown>[];

      if (params.hasFilters) {
        // Use filtered API when search or filters are active
        const queryParams: QueryParams = {
          start: range.start,
          end: range.end,
          sortField: params.sort?.field ?? '',
          sortDesc: params.sort?.descending ?? false,
          filters: params.filters,
          search: params.debouncedSearch,
        };
        rows = await GetResourcesRangeFiltered(JSON.stringify(queryParams));
      } else {
        // Use simple API when no filters
        rows = await GetResourcesRange(
          range.start,
          range.end,
          params.sort?.field ?? '',
          params.sort?.descending ?? false
        );
      }

      if (currentWatchGen !== watchGenRef.current) return;

      // Update visible rows map (handle null/undefined response)
      const fetchedRows = rows ?? [];
      setVisibleRows(prev => {
        const newMap = new Map(prev);
        const changes: CellChange[] = [];
        const now = Date.now();

        // Clear rows outside the new range (with some buffer)
        const clearStart = range.start - overscan * 2;
        const clearEnd = range.end + overscan * 2;
        for (const [idx] of newMap) {
          if (idx < clearStart || idx > clearEnd) {
            newMap.delete(idx);
          }
        }

        // Add new rows and detect cell-level changes
        fetchedRows.forEach((row, i) => {
          const idx = range.start + i;
          const oldRow = prev.get(idx);

          if (oldRow) {
            // Compare same-position rows by key to detect actual changes
            const oldKey = getResourceKey(oldRow);
            const newKey = getResourceKey(row);
            if (oldKey === newKey) {
              const changedPaths = diffFields(oldRow, row);
              for (const columnId of changedPaths) {
                changes.push({ rowId: newKey, columnId, timestamp: now });
              }
            }
          }

          newMap.set(idx, row);
        });

        // Update changedCells for flash animation
        if (changes.length > 0) {
          setChangedCells(changes);
        }

        return newMap;
      });

      // Always mark loading as complete after successful fetch
      setLoading(false);
    } catch (err) {
      console.error('useWindowedData: failed to fetch range:', err);
    } finally {
      pendingFetchRef.current = false;
    }
  }, [gvk, contexts, overscan]);  // totalCount via ref; params accessed via ref

  // Debounced fetch
  const scheduleFetch = useCallback(() => {
    if (fetchTimeoutRef.current) {
      clearTimeout(fetchTimeoutRef.current);
    }
    fetchTimeoutRef.current = setTimeout(() => {
      fetchRange_();
    }, fetchInterval);
  }, [fetchRange_, fetchInterval]);

  // Called when visible range changes (from virtualizer)
  const onVisibleRangeChange = useCallback((startIndex: number, endIndex: number) => {
    visibleRangeRef.current = { start: startIndex, end: endIndex };
    scheduleFetch();
  }, [scheduleFetch]);

  // Watch setup effect
  useEffect(() => {
    if (!gvk || contexts.length === 0) {
      // Only update state if needed to avoid infinite loops
      totalCountRef.current = 0;
      setTotalCount(prev => prev === 0 ? prev : 0);
      setVisibleRows(prev => prev.size === 0 ? prev : new Map());
      setLoading(prev => prev === false ? prev : false);
      setError(prev => prev === null ? prev : null);
      setWatchStatus(prev => prev === 'disconnected' ? prev : 'disconnected');
      return;
    }

    const watchGen = ++watchGenRef.current;

    setError(null);
    setWatchStatus('connecting');
    setInitialSyncComplete(false);  // Reset sync state on new watch
    lastFetchRangeRef.current = { start: -1, end: -1 };

    // Full restart (GVK/context change) - clear everything
    setLoading(true);
    setVisibleRows(new Map());

    // Start watch with current selectedFields
    const fieldsToUse = [...selectedFields];
    StopWatch()
      .catch(() => {})
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        return StartWatch(gvk, contexts, fieldsToUse);
      })
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        setWatchStatus('connected');
        console.log(`useWindowedData: watch started for ${gvk.kind}`);
      })
      .catch((err) => {
        if (watchGen !== watchGenRef.current) return;
        console.error('useWindowedData: failed to start watch:', err);
        setError(err instanceof Error ? err : new Error(String(err)));
        setWatchStatus('error');
        setLoading(false);
      });

    // Subscribe to sync:complete to get initial count
    const unsubscribeSyncComplete = EventsOn('sync:complete', async (data: { count: number }) => {
      if (watchGen !== watchGenRef.current) return;
      console.log(`useWindowedData: sync:complete with ${data.count} items`);

      totalCountRef.current = data.count;
      setTotalCount(data.count);
      setInitialSyncComplete(true);  // Mark initial sync as complete
      if (data.count === 0) {
        setLoading(false);
      } else {
        // Fetch initial visible range (use ref to avoid stale closure)
        fetchRangeRef.current();
      }
    });

    // Subscribe to resource:update for real-time updates
    const unsubscribeResourceUpdate = EventsOn('resource:update', async (event: { type: string; key: string }) => {
      if (watchGen !== watchGenRef.current) return;

      // Refresh count on add/delete
      if (event.type === 'ADDED' || event.type === 'DELETED') {
        try {
          // Use paramsRef to get current search/filter values (avoids stale closure)
          const currentParams = paramsRef.current;
          let count: number;
          if (currentParams.debouncedSearch || currentParams.filters.length > 0) {
            // Use filtered count when filters are active
            const queryParams: QueryParams = {
              start: 0,
              end: 0,
              sortField: '',
              sortDesc: false,
              filters: currentParams.filters,
              search: currentParams.debouncedSearch,
            };
            count = await GetResourceCountFiltered(JSON.stringify(queryParams));
          } else {
            count = await GetResourceCount();
          }
          if (watchGen !== watchGenRef.current) return;
          totalCountRef.current = count;
          setTotalCount(count);
        } catch (err) {
          console.error('useWindowedData: failed to get count:', err);
        }
      }

      // Re-fetch visible range (data might have changed)
      // Reset last fetch range to force re-fetch
      lastFetchRangeRef.current = { start: -1, end: -1 };
      // Use ref-based fetch to avoid stale scheduleFetch/fetchRange_ closure
      if (fetchTimeoutRef.current) {
        clearTimeout(fetchTimeoutRef.current);
      }
      fetchTimeoutRef.current = setTimeout(() => {
        fetchRangeRef.current();
      }, fetchInterval);
    });

    return () => {
      unsubscribeSyncComplete();
      unsubscribeResourceUpdate();
      if (fetchTimeoutRef.current) {
        clearTimeout(fetchTimeoutRef.current);
      }
      setWatchStatus('disconnected');
    };
  }, [gvk, contexts]);  // selectedFields via SetSelectedFields effect; sort/search/filters by re-fetch effect

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      StopWatch().catch(() => {});
    };
  }, []);

  // Handle selectedFields + previewField changes via backend cache
  // Backend SetSelectedFields handles cache miss (LIST + re-extract) without watch restart
  // Also ensures correct fields are applied after watch connects (fixes GVK switch skeleton bug)
  const prevWatchStatusRef = useRef<WatchStatus>('disconnected');
  const prevSelectedFieldsKeyRef = useRef(selectedFieldsKey);
  const prevDebouncedPreviewRef = useRef(debouncedPreview);
  useEffect(() => {
    // Detect watchStatus transition to 'connected' (from any other state)
    const justConnected = prevWatchStatusRef.current !== 'connected' && watchStatus === 'connected';
    prevWatchStatusRef.current = watchStatus;  // Always track status transitions

    const fieldsChanged = prevSelectedFieldsKeyRef.current !== selectedFieldsKey;
    const previewChanged = prevDebouncedPreviewRef.current !== debouncedPreview;

    // Skip if nothing changed AND not just connected
    // When justConnected: always call SetSelectedFields to guarantee backend has correct fields
    // (StartWatch may have captured stale fields during GVK switch)
    if (!justConnected && !fieldsChanged && !previewChanged) return;

    // Don't send if not connected
    if (watchStatus !== 'connected' || !gvk) return;

    // Update field/preview refs when actually calling SetSelectedFields
    prevSelectedFieldsKeyRef.current = selectedFieldsKey;
    prevDebouncedPreviewRef.current = debouncedPreview;

    // Build combined fields: selected + preview
    const allFields = [...selectedFields];
    if (debouncedPreview && !allFields.includes(debouncedPreview)) {
      allFields.push(debouncedPreview);
    }

    console.log('useWindowedData: SetSelectedFields with', allFields.length, 'fields',
      justConnected ? '(just connected)' : '(fields changed)');
    SetSelectedFields(allFields)
      .then((result) => {
        if (result.extracting) {
          // Async extraction in progress → clear stale data so new fields show skeleton
          // instead of '-' (undefined). Re-fetch happens on fields:ready event.
          setExtractingFields(true);
          setVisibleRows(new Map());
        } else {
          // Cache hit → immediately re-fetch
          setExtractingFields(false);
          lastFetchRangeRef.current = { start: -1, end: -1 };
          fetchRangeRef.current();
        }
      })
      .catch((err) => {
        console.error('useWindowedData: SetSelectedFields failed:', err);
      });
  }, [selectedFieldsKey, debouncedPreview, watchStatus, gvk, selectedFields]);

  // Listen for fields:ready event (async extraction complete)
  useEffect(() => {
    const cleanup = EventsOn('fields:ready', () => {
      console.log('useWindowedData: fields:ready received, re-fetching');
      setExtractingFields(false);
      lastFetchRangeRef.current = { start: -1, end: -1 };
      fetchRangeRef.current();
    });
    return cleanup;
  }, []);

  // Track previous search/filter state to detect changes
  const prevSearchFilterRef = useRef({ searchKey: '', filtersKey: '[]', sortKey: '' });

  // Keep fetchRangeRef current
  fetchRangeRef.current = fetchRange_;

  // Re-fetch when sort/search/filters change
  useEffect(() => {
    if (watchStatus !== 'connected') return;

    // Check if search/filter/sort actually changed
    const searchFilterChanged =
      prevSearchFilterRef.current.searchKey !== searchKey ||
      prevSearchFilterRef.current.filtersKey !== filtersKey;
    const sortChanged = prevSearchFilterRef.current.sortKey !== sortKey;

    // Update ref for next comparison
    prevSearchFilterRef.current = { searchKey, filtersKey, sortKey };

    // When search/filters/sort change, clear visible rows (order changed)
    if (searchFilterChanged || sortChanged) {
      setVisibleRows(new Map());
    }
    if (searchFilterChanged) {
      setFilterPending(true);  // Mark filter as pending until count is refreshed
    }

    // When search/filters change, need to re-fetch count and data
    // Use paramsRef to get current values (already updated above)
    const currentParams = paramsRef.current;
    const updateCountAndFetch = async () => {
      try {
        let count: number;
        if (currentParams.debouncedSearch || currentParams.filters.length > 0) {
          const params: QueryParams = {
            start: 0,
            end: 0,
            sortField: '',
            sortDesc: false,
            filters: currentParams.filters,
            search: currentParams.debouncedSearch,
          };
          count = await GetResourceCountFiltered(JSON.stringify(params));
        } else {
          count = await GetResourceCount();
        }
        totalCountRef.current = count;
        setTotalCount(count);
        setFilterPending(false);  // Count refreshed, filter transition complete

        // Force re-fetch by resetting range
        lastFetchRangeRef.current = { start: -1, end: -1 };
        // Directly call fetch using ref (avoids dependency on fetchRange_ function)
        fetchRangeRef.current();
      } catch (err) {
        console.error('useWindowedData: failed to get filtered count:', err);
        setFilterPending(false);
      }
    };

    updateCountAndFetch();
  }, [sortKey, searchKey, filtersKey, watchStatus]);  // Removed fetchRange_, debouncedSearch, filters - accessed via refs

  // Get row data by index
  const getRowData = useCallback((index: number) => {
    return visibleRows.get(index);
  }, [visibleRows]);

  // Get row ID
  const getRowId = useCallback((row: Record<string, unknown>) => {
    return getResourceKey(row);
  }, []);

  // Manual refresh
  const refresh = useCallback(async () => {
    if (!gvk || contexts.length === 0) return;

    setLoading(true);
    setVisibleRows(new Map());
    lastFetchRangeRef.current = { start: -1, end: -1 };

    try {
      // Re-fetch count (with filters if active)
      let count: number;
      if (hasFilters) {
        const params: QueryParams = {
          start: 0,
          end: 0,
          sortField: '',
          sortDesc: false,
          filters: filters,
          search: debouncedSearch,
        };
        count = await GetResourceCountFiltered(JSON.stringify(params));
      } else {
        count = await GetResourceCount();
      }
      totalCountRef.current = count;
      setTotalCount(count);
      await fetchRange_();
    } catch (err) {
      console.error('useWindowedData: refresh failed:', err);
      setError(err instanceof Error ? err : new Error(String(err)));
    } finally {
      setLoading(false);
    }
  }, [gvk, contexts, fetchRange_, hasFilters, debouncedSearch, filters]);

  return {
    totalCount,
    visibleRows,
    loading,
    filterPending,
    error,
    watchStatus,
    initialSyncComplete,
    extractingFields,
    changedCells,
    getRowData,
    getRowId,
    onVisibleRangeChange,
    fetchRange,
    refresh,
  };
}
