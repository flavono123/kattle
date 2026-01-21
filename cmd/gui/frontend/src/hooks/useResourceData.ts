import { useState, useEffect, useCallback, useRef } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { StartWatch, StopWatch, GetResourcesByKeys, GetAllResourceKeys, SetSelectedFields } from '../../wailsjs/go/main/App';
import type { main } from '../../wailsjs/go/models';
import { getResourceKey, type ResourceEventMeta, type CellChange, diffFields } from '../lib/resource-utils';

// Watch connection status
export type WatchStatus = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface UseResourceDataOptions {
  /** Enable real-time updates via watch (default: true) */
  watch?: boolean;
  /** Batch interval in ms for fetching resources (default: 100) */
  batchInterval?: number;
  /** Selected field paths to extract (e.g., "status.phase", "spec.replicas").
   * Pass this to enable memory-efficient selective field extraction.
   * Without this, only essential metadata fields are returned. */
  selectedFields?: string[];
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
  const { watch = true, batchInterval = 100, selectedFields = [] } = options;

  // Serialize selectedFields for stable dependency comparison (avoid restart on same content, different reference)
  const selectedFieldsKey = JSON.stringify(selectedFields);

  const [data, setData] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [watchStatus, setWatchStatus] = useState<WatchStatus>('disconnected');
  const [changedCells, setChangedCells] = useState<CellChange[]>([]);

  // Track watch generation to avoid stale operations
  const watchGenRef = useRef(0);

  // Track previous GVK and contexts to detect what changed
  // This allows preserving data when only selectedFields changes
  const prevGvkRef = useRef<main.MultiClusterGVK | null>(null);
  const prevContextsKeyRef = useRef<string>('');

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
      prevGvkRef.current = null;
      prevContextsKeyRef.current = '';
      return;
    }

    // Determine what changed to decide whether to clear data
    const currentContextsKey = JSON.stringify(contexts);
    const isGvkChanged = !prevGvkRef.current ||
      gvk.group !== prevGvkRef.current.group ||
      gvk.version !== prevGvkRef.current.version ||
      gvk.kind !== prevGvkRef.current.kind;
    const isContextsChanged = currentContextsKey !== prevContextsKeyRef.current;
    const shouldClearData = isGvkChanged || isContextsChanged;

    // Update refs for next comparison
    prevGvkRef.current = gvk;
    prevContextsKeyRef.current = currentContextsKey;

    const watchGen = ++watchGenRef.current;
    setError(null);

    // Only clear data if GVK or contexts changed, NOT when only selectedFields changes
    // This preserves existing data during field selection changes
    if (shouldClearData) {
      console.log('useResourceData: clearing data (GVK or contexts changed)');
      setData([]);
      setLoading(true);
    } else {
      // When only selectedFields changes, don't show loading since data is already visible
      // The batch processor will update data incrementally
      console.log('useResourceData: preserving data (only selectedFields changed)');
    }

    setWatchStatus('connecting');
    pendingKeys.current.clear();
    pendingDeletes.current.clear();
    // Always reset these flags when starting a new watch - flush will set loading=false after first batch
    hasReceivedFirstBatch.current = false;
    hasReceivedAnyEvent.current = false;

    // Initial sync timeout - if no data arrives within this time, assume sync is complete
    let initialSyncTimeout: ReturnType<typeof setTimeout> | null = null;

    // Chain the start operation to ensure previous stop completes first
    const startOperation = watchOperationRef.current
      .then(() => StopWatch().catch(() => {})) // Stop any existing watch first, ignore errors
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        return StartWatch(gvk, contexts, selectedFields);
      })
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        // StartWatch now returns immediately (async behavior).
        // Initial sync happens in background - wait for sync:complete event.
        console.log(`useResourceData: watch setup complete for ${gvk.kind}, waiting for sync:complete`);
        setWatchStatus('connected');

        // Set a longer timeout for truly empty resources (no sync:complete within 5s)
        initialSyncTimeout = setTimeout(() => {
          if (watchGen !== watchGenRef.current) return;
          if (!hasReceivedFirstBatch.current && !hasReceivedAnyEvent.current) {
            console.log(`useResourceData: sync timeout for ${gvk.kind}, assuming 0 resources`);
            setLoading(false);
            hasReceivedFirstBatch.current = true;
          }
        }, 5000);
      })
      .catch((err) => {
        if (watchGen !== watchGenRef.current) return;
        console.error('useResourceData: failed to start watch:', err);
        setError(err instanceof Error ? err : new Error(String(err)));
        setWatchStatus('error');
        setLoading(false);
      });

    watchOperationRef.current = startOperation;

    // Subscribe to sync:progress event (emitted every 100 items during sync)
    // This allows progressive loading without overwhelming JS with 6000+ individual events
    const unsubscribeSyncProgress = EventsOn('sync:progress', async (data: { count: number }) => {
      if (watchGen !== watchGenRef.current) {
        console.log(`useResourceData: sync:progress SKIPPED (watchGen mismatch: ${watchGen} vs ${watchGenRef.current})`);
        return;
      }

      console.log(`useResourceData: sync:progress received with ${data.count} items for ${gvk.kind}, watchGen=${watchGen}`);
      hasReceivedAnyEvent.current = true;

      // Fetch all available keys from FieldStore for progressive loading
      try {
        const allKeys = await GetAllResourceKeys();
        if (watchGen !== watchGenRef.current) {
          console.log(`useResourceData: sync:progress - SKIPPED after fetch (watchGen changed)`);
          return;
        }
        if (allKeys && allKeys.length > 0) {
          console.log(`useResourceData: sync:progress - fetched ${allKeys.length} keys, adding to pendingKeys (current size: ${pendingKeys.current.size})`);
          for (const key of allKeys) {
            pendingKeys.current.add(key);
          }
          console.log(`useResourceData: sync:progress - pendingKeys size after add: ${pendingKeys.current.size}`);
        }
      } catch (err) {
        console.error('useResourceData: failed to get keys during sync:progress:', err);
      }
    });

    // Subscribe to sync:complete event (emitted when initial sync finishes)
    const unsubscribeSyncComplete = EventsOn('sync:complete', async (data: { count: number }) => {
      if (watchGen !== watchGenRef.current) return;

      console.log(`useResourceData: sync:complete received with ${data.count} items for ${gvk.kind}`);
      hasReceivedAnyEvent.current = true;

      // Clear the timeout since sync completed
      if (initialSyncTimeout) {
        clearTimeout(initialSyncTimeout);
        initialSyncTimeout = null;
      }

      // Fetch final keys after sync complete
      try {
        const allKeys = await GetAllResourceKeys();
        if (watchGen !== watchGenRef.current) return;
        if (allKeys && allKeys.length > 0) {
          console.log(`useResourceData: sync:complete - fetched ${allKeys.length} final keys`);
          for (const key of allKeys) {
            pendingKeys.current.add(key);
          }
        } else if (data.count === 0) {
          // No resources - complete loading immediately
          console.log(`useResourceData: sync:complete with 0 items, marking loading complete`);
          setLoading(false);
          hasReceivedFirstBatch.current = true;
        }
      } catch (err) {
        console.error('useResourceData: failed to get keys on sync:complete:', err);
        setLoading(false);
      }
    });

    // Subscribe to lightweight events (Pull Model) for real-time updates
    const unsubscribeResourceUpdate = EventsOn('resource:update', (event: ResourceEventMeta) => {
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
      unsubscribeSyncProgress();
      unsubscribeSyncComplete();
      unsubscribeResourceUpdate();
      setWatchStatus('disconnected');
    };
    // NOTE: selectedFieldsKey is intentionally NOT a dependency here.
    // Changing selectedFields should NOT restart the watch (which clears FieldStore).
    // Instead, a separate effect handles selectedFields updates via SetSelectedFields API.
  }, [watch, gvk, contexts]);

  // Cleanup on unmount - ensure watch is stopped
  useEffect(() => {
    return () => {
      StopWatch()
        .then(() => console.log('useResourceData: watch stopped on unmount'))
        .catch(() => {}); // Ignore errors on unmount
    };
  }, []);

  // Track initial selectedFields to skip the first run (initial selectedFields passed to StartWatch)
  const initialSelectedFieldsKeyRef = useRef<string | null>(null);

  // Handle selectedFields changes WITHOUT restarting watch
  // This effect updates the backend field list and queues a re-fetch of all existing data
  useEffect(() => {
    // Skip if watch is not active
    if (!watch || !gvk || watchStatus !== 'connected') {
      return;
    }

    // Skip the first run - initial selectedFields are already passed to StartWatch
    if (initialSelectedFieldsKeyRef.current === null) {
      initialSelectedFieldsKeyRef.current = selectedFieldsKey;
      return;
    }

    // Skip if selectedFields haven't actually changed
    if (initialSelectedFieldsKeyRef.current === selectedFieldsKey) {
      return;
    }

    // Update ref for next comparison
    initialSelectedFieldsKeyRef.current = selectedFieldsKey;

    console.log('useResourceData: selectedFields changed, updating backend without watch restart');

    // Update backend selectedFields (new/modified resources will use new fields)
    SetSelectedFields(selectedFields)
      .then(async () => {
        // Queue all existing keys for re-fetch to get data with new field extraction
        // Note: Backend re-extracts data from FieldStore when fields change
        try {
          const allKeys = await GetAllResourceKeys();
          if (allKeys && allKeys.length > 0) {
            console.log(`useResourceData: queuing ${allKeys.length} keys for re-fetch with new fields`);
            for (const key of allKeys) {
              pendingKeys.current.add(key);
            }
          }
        } catch (err) {
          console.error('useResourceData: failed to get keys for re-fetch:', err);
        }
      })
      .catch((err) => {
        console.error('useResourceData: failed to update selectedFields:', err);
      });
  }, [watch, gvk, watchStatus, selectedFieldsKey, selectedFields]);

  // Batch processor: fetch resources and apply updates (Pull Model)
  // Chunk size for fetching resources - prevents overwhelming the system with large batches
  const FETCH_CHUNK_SIZE = 500;

  useEffect(() => {
    if (!watch || !gvk) return;

    // Capture current watchGen to detect GVK changes during async operations
    const currentWatchGen = watchGenRef.current;

    const flush = async () => {
      // Check if GVK has changed since this effect started
      if (currentWatchGen !== watchGenRef.current) {
        console.log(`useResourceData flush: skipping (watchGen mismatch: ${currentWatchGen} vs ${watchGenRef.current})`);
        return;
      }

      // Take only a chunk of pending keys to avoid overwhelming the system
      const allPendingKeys = Array.from(pendingKeys.current);
      const keysToFetch = allPendingKeys.slice(0, FETCH_CHUNK_SIZE);
      const keysToDelete = Array.from(pendingDeletes.current);

      // Debug log to see if flush is being called with keys
      if (allPendingKeys.length > 0 || keysToDelete.length > 0) {
        console.log(`useResourceData flush: pending=${allPendingKeys.length}, toFetch=${keysToFetch.length}, toDelete=${keysToDelete.length}`);
      }

      // Remove fetched keys from pending (keep remaining for next batch)
      for (const key of keysToFetch) {
        pendingKeys.current.delete(key);
      }
      pendingDeletes.current.clear();

      // Nothing to do
      if (keysToFetch.length === 0 && keysToDelete.length === 0) {
        return;
      }

      // Log progress for large batches
      if (allPendingKeys.length > FETCH_CHUNK_SIZE) {
        console.log(`useResourceData: fetching chunk ${keysToFetch.length}/${allPendingKeys.length} keys`);
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
        return StartWatch(gvk, contexts, selectedFields);
      })
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        setWatchStatus('connected');
        console.log(`useResourceData: refresh - watch setup complete, waiting for sync:complete`);

        // Wait for sync:complete event with one-time listener
        // NOTE: Events are now emitted DURING sync, so data is shown progressively.
        // sync:complete just signals initial list is complete.
        return new Promise<void>((resolve) => {
          let timeoutId: ReturnType<typeof setTimeout> | null = null;

          const unsubscribe = EventsOn('sync:complete', (data: { count: number }) => {
            if (watchGen !== watchGenRef.current) {
              unsubscribe();
              if (timeoutId) clearTimeout(timeoutId);
              resolve();
              return;
            }

            unsubscribe(); // One-time listener
            if (timeoutId) clearTimeout(timeoutId);

            console.log(`useResourceData: refresh - sync:complete with ${data.count} items`);
            hasReceivedAnyEvent.current = true;

            // If no data, mark loading complete immediately
            if (data.count === 0) {
              setLoading(false);
              hasReceivedFirstBatch.current = true;
            }
            // Otherwise, batch processor will set loading=false after first batch

            resolve();
          });

          // Timeout fallback for truly empty resources
          timeoutId = setTimeout(() => {
            if (watchGen !== watchGenRef.current) {
              unsubscribe();
              resolve();
              return;
            }
            console.log(`useResourceData: refresh - sync timeout, assuming 0 resources`);
            unsubscribe();
            if (!hasReceivedFirstBatch.current && !hasReceivedAnyEvent.current) {
              setLoading(false);
              hasReceivedFirstBatch.current = true;
            }
            resolve();
          }, 5000);
        });
      })
      .catch((err) => {
        if (watchGen !== watchGenRef.current) return;
        setError(err instanceof Error ? err : new Error(String(err)));
        setLoading(false);
      });

    watchOperationRef.current = refreshOperation;
  }, [gvk, contexts, selectedFields]);

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
