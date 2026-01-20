import { useState, useEffect, useCallback, useRef } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { StartWatch, StopWatch, GetResourcesByKeys, GetAllResourceKeys } from '../../wailsjs/go/main/App';
import type { main } from '../../wailsjs/go/models';
import { getResourceKey, type ResourceEventMeta, type CellChange, diffFields } from '../lib/resource-utils';

// Watch connection status
export type WatchStatus = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface UseResourceDataOptions {
  /** Enable real-time updates via watch (default: true) */
  watch?: boolean;
  /** Batch interval in ms for fetching resources (default: 100) */
  batchInterval?: number;
}

export interface UseResourceDataResult {
  /** Resource data array */
  data: any[];
  /** Loading state for initial fetch */
  loading: boolean;
  /** Error from fetch or watch */
  error: Error | null;
  /** Manually refresh data (restarts watch) */
  refresh: () => void;
  /** Watch connection status */
  watchStatus: WatchStatus;
  /** Get stable row ID for TanStack Table */
  getRowId: (row: any) => string;
  /** Cells that changed in the most recent batch update */
  changedCells: CellChange[];
}

/**
 * Hook for fetching and managing Kubernetes resource data via watch (Pull Model)
 *
 * Pull Model:
 * 1. Backend emits lightweight events (type + key only) via EventsEmit
 * 2. Frontend collects keys and fetches full objects via GetResourcesByKeys
 * 3. This avoids WebView memory leaks caused by eval() with large objects
 *
 * @param gvk The Group/Version/Kind to fetch
 * @param contexts Array of Kubernetes contexts to query
 * @param options Configuration options
 */
export function useResourceData(
  gvk: main.MultiClusterGVK | null,
  contexts: string[],
  options: UseResourceDataOptions = {}
): UseResourceDataResult {
  const { watch = true, batchInterval = 100 } = options;

  const [data, setData] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [watchStatus, setWatchStatus] = useState<WatchStatus>('disconnected');
  const [changedCells, setChangedCells] = useState<CellChange[]>([]);

  // Track watch generation to avoid stale operations
  const watchGenRef = useRef(0);

  // Pending keys for ADDED/MODIFIED events (Pull Model)
  const pendingKeys = useRef<Set<string>>(new Set());

  // Pending deletes - keys to remove
  const pendingDeletes = useRef<Set<string>>(new Set());

  // Flag to track if first batch has been received
  const hasReceivedFirstBatch = useRef(false);

  // Flag to track if any events have been received (for timeout decision)
  const hasReceivedAnyEvent = useRef(false);

  // Promise to serialize StopWatch/StartWatch operations
  const watchOperationRef = useRef<Promise<void>>(Promise.resolve());

  // Watch subscription
  useEffect(() => {
    if (!watch || !gvk || contexts.length === 0) {
      setData([]);
      setLoading(false);
      setError(null);
      setWatchStatus('disconnected');
      return;
    }

    const watchGen = ++watchGenRef.current;
    setLoading(true);
    setError(null);
    setData([]);
    setWatchStatus('connecting');
    pendingKeys.current.clear();
    pendingDeletes.current.clear();
    hasReceivedFirstBatch.current = false;
    hasReceivedAnyEvent.current = false;

    // Initial sync timeout - if no data arrives within this time, assume sync is complete
    let initialSyncTimeout: ReturnType<typeof setTimeout> | null = null;

    // Chain the start operation to ensure previous stop completes first
    const startOperation = watchOperationRef.current
      .then(() => StopWatch().catch(() => {})) // Stop any existing watch first, ignore errors
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        return StartWatch(gvk, contexts);
      })
      .then(async () => {
        if (watchGen !== watchGenRef.current) return;
        console.log(`useResourceData: watch connected for ${gvk.kind}`);
        setWatchStatus('connected');

        // Fetch all keys from FieldStore after initial sync completes.
        // This is critical because trySend drops events when buffer is full during initial sync.
        // Without this, only a fraction of resources would be displayed.
        try {
          const allKeys = await GetAllResourceKeys();
          if (watchGen !== watchGenRef.current) return;
          if (allKeys && allKeys.length > 0) {
            console.log(`useResourceData: fetched ${allKeys.length} initial keys for ${gvk.kind}`);
            hasReceivedAnyEvent.current = true;
            for (const key of allKeys) {
              pendingKeys.current.add(key);
            }
          }
        } catch (err) {
          console.error('useResourceData: failed to get initial keys:', err);
        }

        // Set timeout to complete loading only if no events have been received
        // This handles GVKs with 0 resources
        initialSyncTimeout = setTimeout(() => {
          if (watchGen !== watchGenRef.current) return;
          // Only trigger timeout if no events at all (truly 0 resources)
          if (!hasReceivedFirstBatch.current && !hasReceivedAnyEvent.current) {
            console.log(`useResourceData: initial sync timeout for ${gvk.kind}, no events received, completing loading`);
            setLoading(false);
            hasReceivedFirstBatch.current = true;
          }
        }, 500);
      })
      .catch((err) => {
        if (watchGen !== watchGenRef.current) return;
        console.error('useResourceData: failed to start watch:', err);
        setError(err instanceof Error ? err : new Error(String(err)));
        setWatchStatus('error');
        setLoading(false);
      });

    watchOperationRef.current = startOperation;

    // Subscribe to lightweight events (Pull Model)
    const unsubscribe = EventsOn('resource:update', (event: ResourceEventMeta) => {
      if (watchGen !== watchGenRef.current) return;

      // Mark that we've received at least one event (for timeout decision)
      hasReceivedAnyEvent.current = true;

      if (event.type === 'DELETED') {
        // Collect deletes separately
        pendingDeletes.current.add(event.key);
        pendingKeys.current.delete(event.key); // No need to fetch deleted resources
      } else {
        // ADDED or MODIFIED - collect key for batch fetch
        pendingKeys.current.add(event.key);
      }
    });

    // Cleanup - only clear timeout and unsubscribe
    // StopWatch is called in the chained operation to avoid race conditions
    return () => {
      if (initialSyncTimeout) {
        clearTimeout(initialSyncTimeout);
      }
      unsubscribe();
      setWatchStatus('disconnected');
    };
  }, [watch, gvk, contexts]);

  // Cleanup on unmount - ensure watch is stopped
  useEffect(() => {
    return () => {
      StopWatch()
        .then(() => console.log('useResourceData: watch stopped on unmount'))
        .catch(() => {}); // Ignore errors on unmount
    };
  }, []);

  // Batch processor: fetch resources and apply updates (Pull Model)
  useEffect(() => {
    if (!watch || !gvk) return;

    // Capture current watchGen to detect GVK changes during async operations
    const currentWatchGen = watchGenRef.current;

    const flush = async () => {
      // Check if GVK has changed since this effect started
      if (currentWatchGen !== watchGenRef.current) return;

      const keysToFetch = Array.from(pendingKeys.current);
      const keysToDelete = Array.from(pendingDeletes.current);

      // Clear pending sets
      pendingKeys.current.clear();
      pendingDeletes.current.clear();

      // Nothing to do
      if (keysToFetch.length === 0 && keysToDelete.length === 0) {
        return;
      }

      // Fetch resources for ADDED/MODIFIED keys
      let fetchedResources: any[] = [];
      if (keysToFetch.length > 0) {
        try {
          fetchedResources = await GetResourcesByKeys(keysToFetch);
        } catch (err) {
          console.error('useResourceData: failed to fetch resources:', err);
          return;
        }
      }

      // Check again after async operation in case GVK changed during fetch
      if (currentWatchGen !== watchGenRef.current) return;

      // Apply updates to data
      setData((prev) => {
        const now = Date.now();
        const changes: CellChange[] = [];

        // Build map from current data
        const dataMap = new Map(prev.map((item) => [getResourceKey(item), item]));

        // Apply deletes
        for (const key of keysToDelete) {
          dataMap.delete(key);
        }

        // Apply fetched resources (ADDED/MODIFIED)
        for (const resource of fetchedResources) {
          const rowId = getResourceKey(resource);
          const prevResource = dataMap.get(rowId);

          // Track changes for MODIFIED
          if (prevResource) {
            const changedPaths = diffFields(prevResource, resource);
            for (const columnId of changedPaths) {
              changes.push({ rowId, columnId, timestamp: now });
            }
          }

          dataMap.set(rowId, resource);
        }

        // Update changed cells
        if (changes.length > 0) {
          setChangedCells(changes);
        }

        return Array.from(dataMap.values());
      });

      // Loading complete after first batch with data
      if (!hasReceivedFirstBatch.current) {
        hasReceivedFirstBatch.current = true;
        setLoading(false);
      }
    };

    const timer = setInterval(flush, batchInterval);
    return () => clearInterval(timer);
  }, [watch, batchInterval, gvk]);

  // Manual refresh - restarts the watch
  const refresh = useCallback(() => {
    if (!gvk || contexts.length === 0) return;

    const watchGen = ++watchGenRef.current;
    setLoading(true);
    setError(null);
    setData([]);
    pendingKeys.current.clear();
    pendingDeletes.current.clear();
    hasReceivedFirstBatch.current = false;
    hasReceivedAnyEvent.current = false;

    // Chain the operation to avoid race conditions
    const refreshOperation = watchOperationRef.current
      .then(() => StopWatch().catch(() => {}))
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        return StartWatch(gvk, contexts);
      })
      .then(async () => {
        if (watchGen !== watchGenRef.current) return;
        setWatchStatus('connected');

        // Fetch all keys from FieldStore after initial sync completes
        try {
          const allKeys = await GetAllResourceKeys();
          if (watchGen !== watchGenRef.current) return;
          if (allKeys && allKeys.length > 0) {
            hasReceivedAnyEvent.current = true;
            for (const key of allKeys) {
              pendingKeys.current.add(key);
            }
          }
        } catch (err) {
          console.error('useResourceData: failed to get initial keys on refresh:', err);
        }

        // Set timeout to complete loading only if no events received
        setTimeout(() => {
          if (watchGen !== watchGenRef.current) return;
          if (!hasReceivedFirstBatch.current && !hasReceivedAnyEvent.current) {
            setLoading(false);
            hasReceivedFirstBatch.current = true;
          }
        }, 500);
      })
      .catch((err) => {
        if (watchGen !== watchGenRef.current) return;
        setError(err instanceof Error ? err : new Error(String(err)));
        setLoading(false);
      });

    watchOperationRef.current = refreshOperation;
  }, [gvk, contexts]);

  // Stable row ID function for TanStack Table
  const getRowId = useCallback((row: any) => {
    return getResourceKey(row);
  }, []);

  return {
    data,
    loading,
    error,
    refresh,
    watchStatus,
    getRowId,
    changedCells,
  };
}
