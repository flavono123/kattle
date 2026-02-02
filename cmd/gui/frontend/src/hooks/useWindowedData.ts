import { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import {
  StartWatch,
  StopWatch,
  GetResourceCount,
  GetResourcesRange,
} from '../../wailsjs/go/main/App';
import type { main } from '../../wailsjs/go/models';
import { getResourceKey } from '../lib/resource-utils';

export type WatchStatus = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface SortConfig {
  field: string;      // e.g., "metadata.creationTimestamp"
  descending: boolean;
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
}

export interface UseWindowedDataResult {
  /** Total count of resources (for virtualizer) */
  totalCount: number;
  /** Currently loaded rows (sparse - only visible range) */
  visibleRows: Map<number, Record<string, unknown>>;
  /** Loading state */
  loading: boolean;
  /** Error from fetch or watch */
  error: Error | null;
  /** Watch connection status */
  watchStatus: WatchStatus;
  /** Get row data by virtual index (returns undefined if not loaded) */
  getRowData: (index: number) => Record<string, unknown> | undefined;
  /** Get stable row ID */
  getRowId: (row: Record<string, unknown>) => string;
  /** Notify when visible range changes */
  onVisibleRangeChange: (startIndex: number, endIndex: number) => void;
  /** Current fetch range (for debugging) */
  fetchRange: { start: number; end: number };
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
  } = options;

  const selectedFieldsKey = JSON.stringify(selectedFields);
  const sortKey = sort ? `${sort.field}:${sort.descending}` : '';

  // State
  const [totalCount, setTotalCount] = useState(0);
  const [visibleRows, setVisibleRows] = useState<Map<number, Record<string, unknown>>>(new Map());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [watchStatus, setWatchStatus] = useState<WatchStatus>('disconnected');

  // Refs for mutable state
  const watchGenRef = useRef(0);
  const visibleRangeRef = useRef({ start: 0, end: 50 });
  const pendingFetchRef = useRef(false);
  const lastFetchRangeRef = useRef({ start: -1, end: -1 });
  const fetchTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Computed fetch range (visible + overscan)
  const fetchRange = useMemo(() => ({
    start: Math.max(0, visibleRangeRef.current.start - overscan),
    end: visibleRangeRef.current.end + overscan,
  }), [visibleRangeRef.current.start, visibleRangeRef.current.end, overscan]);

  // Fetch data for current range
  const fetchRange_ = useCallback(async () => {
    if (!gvk || contexts.length === 0) return;

    const currentWatchGen = watchGenRef.current;
    const range = {
      start: Math.max(0, visibleRangeRef.current.start - overscan),
      end: Math.min(totalCount, visibleRangeRef.current.end + overscan),
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
      const rows = await GetResourcesRange(
        range.start,
        range.end,
        sort?.field ?? '',
        sort?.descending ?? false
      );

      if (currentWatchGen !== watchGenRef.current) return;

      // Update visible rows map (handle null/undefined response)
      const fetchedRows = rows ?? [];
      setVisibleRows(prev => {
        const newMap = new Map(prev);

        // Clear rows outside the new range (with some buffer)
        const clearStart = range.start - overscan * 2;
        const clearEnd = range.end + overscan * 2;
        for (const [idx] of newMap) {
          if (idx < clearStart || idx > clearEnd) {
            newMap.delete(idx);
          }
        }

        // Add new rows
        fetchedRows.forEach((row, i) => {
          newMap.set(range.start + i, row);
        });

        return newMap;
      });

      if (loading) {
        setLoading(false);
      }
    } catch (err) {
      console.error('useWindowedData: failed to fetch range:', err);
    } finally {
      pendingFetchRef.current = false;
    }
  }, [gvk, contexts, totalCount, overscan, sort, loading]);

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
      setTotalCount(prev => prev === 0 ? prev : 0);
      setVisibleRows(prev => prev.size === 0 ? prev : new Map());
      setLoading(prev => prev === false ? prev : false);
      setError(prev => prev === null ? prev : null);
      setWatchStatus(prev => prev === 'disconnected' ? prev : 'disconnected');
      return;
    }

    const watchGen = ++watchGenRef.current;
    setError(null);
    setLoading(true);
    setWatchStatus('connecting');
    setVisibleRows(new Map());
    lastFetchRangeRef.current = { start: -1, end: -1 };

    // Start watch
    StopWatch()
      .catch(() => {})
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        return StartWatch(gvk, contexts, selectedFields);
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

      setTotalCount(data.count);
      if (data.count === 0) {
        setLoading(false);
      } else {
        // Fetch initial visible range
        fetchRange_();
      }
    });

    // Subscribe to resource:update for real-time updates
    const unsubscribeResourceUpdate = EventsOn('resource:update', async (event: { type: string; key: string }) => {
      if (watchGen !== watchGenRef.current) return;

      // Refresh count on add/delete
      if (event.type === 'ADDED' || event.type === 'DELETED') {
        try {
          const count = await GetResourceCount();
          if (watchGen !== watchGenRef.current) return;
          setTotalCount(count);
        } catch (err) {
          console.error('useWindowedData: failed to get count:', err);
        }
      }

      // Re-fetch visible range (data might have changed)
      // Reset last fetch range to force re-fetch
      lastFetchRangeRef.current = { start: -1, end: -1 };
      scheduleFetch();
    });

    return () => {
      unsubscribeSyncComplete();
      unsubscribeResourceUpdate();
      if (fetchTimeoutRef.current) {
        clearTimeout(fetchTimeoutRef.current);
      }
      setWatchStatus('disconnected');
    };
  }, [gvk, contexts, selectedFieldsKey, sortKey]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      StopWatch().catch(() => {});
    };
  }, []);

  // Re-fetch when sort changes
  useEffect(() => {
    if (watchStatus !== 'connected') return;

    // Clear cached rows and re-fetch
    setVisibleRows(new Map());
    lastFetchRangeRef.current = { start: -1, end: -1 };
    scheduleFetch();
  }, [sortKey, watchStatus, scheduleFetch]);

  // Get row data by index
  const getRowData = useCallback((index: number) => {
    return visibleRows.get(index);
  }, [visibleRows]);

  // Get row ID
  const getRowId = useCallback((row: Record<string, unknown>) => {
    return getResourceKey(row);
  }, []);

  // Manual refresh
  const refresh = useCallback(() => {
    if (!gvk || contexts.length === 0) return;

    setLoading(true);
    setVisibleRows(new Map());
    lastFetchRangeRef.current = { start: -1, end: -1 };

    // Re-fetch count and data
    GetResourceCount()
      .then(count => {
        setTotalCount(count);
        return fetchRange_();
      })
      .catch(err => {
        console.error('useWindowedData: refresh failed:', err);
        setError(err instanceof Error ? err : new Error(String(err)));
      })
      .finally(() => {
        setLoading(false);
      });
  }, [gvk, contexts, fetchRange_]);

  return {
    totalCount,
    visibleRows,
    loading,
    error,
    watchStatus,
    getRowData,
    getRowId,
    onVisibleRangeChange,
    fetchRange,
    refresh,
  };
}
