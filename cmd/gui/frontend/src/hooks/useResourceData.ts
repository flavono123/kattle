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
  /** Preview field path (for hover preview). Debounced and fetched to show actual values. */
  previewField?: string;
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
  /** Field paths currently being loaded (for skeleton UI) */
  loadingFields: Set<string>;
  /** Field paths that have been extracted from backend (for skeleton decision) */
  extractedFields: Set<string>;
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
  const { watch = true, batchInterval = 100, selectedFields = [], previewField } = options;

  // Serialize selectedFields for stable dependency comparison (avoid restart on same content, different reference)
  const selectedFieldsKey = JSON.stringify(selectedFields);

  // Debounced preview field - stabilizes after 250ms of no changes
  const [debouncedPreviewField, setDebouncedPreviewField] = useState<string | undefined>(undefined);

  // Debounce previewField changes (200ms for hover to feel responsive)
  useEffect(() => {
    if (!previewField) {
      // Don't clear immediately - keep the last preview field in memory
      return;
    }

    const timer = setTimeout(() => {
      setDebouncedPreviewField(previewField);
    }, 200);

    return () => clearTimeout(timer);
  }, [previewField]);

  const [data, setData] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [watchStatus, setWatchStatus] = useState<WatchStatus>('disconnected');
  const [changedCells, setChangedCells] = useState<CellChange[]>([]);

  // State to force main effect re-run when watch is restarted for field changes (SQLStore mode)
  const [fieldChangeRestartKey, setFieldChangeRestartKey] = useState(0);

  // Track fields currently being loaded (for cell-level skeleton UI)
  const [loadingFields, setLoadingFields] = useState<Set<string>>(new Set());

  // Track which fields have been extracted (for skeleton decision in UI)
  // Fields not in this set have no data yet and should show skeleton
  const [extractedFields, setExtractedFields] = useState<Set<string>>(new Set());

  // Ref version of extractedFields for use in effects (avoids stale closure issues)
  const extractedFieldsRef = useRef<Set<string>>(new Set());

  // Track if current restart is for field change only (skip full loading, use cell skeleton instead)
  const isFieldChangeRestartRef = useRef(false);

  // Track watch generation to avoid stale operations
  const watchGenRef = useRef(0);

  // Track previous GVK and contexts to detect what changed
  // This allows preserving data when only selectedFields changes
  const prevGvkRef = useRef<main.MultiClusterGVK | null>(null);
  const prevContextsKeyRef = useRef<string>('');

  // Pending keys for ADDED/MODIFIED events (Pull Model - fallback when no fields in event)
  const pendingKeys = useRef<Set<string>>(new Set());

  // Pending delta updates - resources with fields included in event (no GetResourcesByKeys needed)
  const pendingDeltaUpdates = useRef<Map<string, Record<string, unknown>>>(new Map());

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
      // Only update state if needed to avoid infinite loops
      setData(prev => prev.length === 0 ? prev : []);
      setLoading(prev => prev === false ? prev : false);
      setError(prev => prev === null ? prev : null);
      setWatchStatus(prev => prev === 'disconnected' ? prev : 'disconnected');
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

    // Reset field-change restart flag (consumed by this run)
    isFieldChangeRestartRef.current = false;

    const watchGen = ++watchGenRef.current;
    setError(null);

    // Show loading only for full restarts (GVK/context change).
    // Field-change restarts use cell-level skeleton UI instead of full loading spinner.
    if (shouldClearData) {
      setLoading(true);
    }

    // Only clear data if GVK or contexts changed
    if (shouldClearData) {
      console.log('useResourceData: clearing data (GVK or contexts changed)');
      setData([]);
      // Clear loadingFields and extractedFields on full reset
      setLoadingFields(new Set());
      setExtractedFields(new Set());
      extractedFieldsRef.current = new Set();
    }

    // Only change watchStatus if this is a full restart (not just a field/preview change)
    // This prevents brief "no resources" flash during field changes
    if (shouldClearData) {
      setWatchStatus('connecting');
    }
    pendingKeys.current.clear();
    pendingDeltaUpdates.current.clear();
    pendingDeletes.current.clear();
    // Always reset these flags when starting a new watch - flush will set loading=false after first batch
    hasReceivedFirstBatch.current = false;
    hasReceivedAnyEvent.current = false;

    // Fields to extract (preview field is handled by a separate effect)
    const allFieldsToExtract = selectedFields;

    // Chain the start operation to ensure previous stop completes first
    const startOperation = watchOperationRef.current
      .then(() => StopWatch().catch(() => {})) // Stop any existing watch first, ignore errors
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        // Track which fields are extracted in this watch session (including preview field)
        // This is used by UI to show skeleton for fields not yet extracted
        const newExtractedFields = new Set(allFieldsToExtract);
        extractedFieldsRef.current = newExtractedFields;
        setExtractedFields(newExtractedFields);
        return StartWatch(gvk, contexts, allFieldsToExtract);
      })
      .then(() => {
        if (watchGen !== watchGenRef.current) return;
        // StartWatch now returns immediately (async behavior).
        // Initial sync happens in background - wait for sync:complete event.
        console.log(`useResourceData: watch setup complete for ${gvk.kind}, waiting for sync:complete`);
        setWatchStatus('connected');
        // No timeout - loading state is managed by:
        // 1. flush() after first batch of data
        // 2. sync:complete with count=0 for empty resources
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

    // Subscribe to events for real-time updates (Delta Update Model)
    // If event includes fields, apply directly without GetResourcesByKeys call
    const unsubscribeResourceUpdate = EventsOn('resource:update', (event: ResourceEventMeta) => {
      if (watchGen !== watchGenRef.current) return;

      // Mark that we've received at least one event (for timeout decision)
      hasReceivedAnyEvent.current = true;

      if (event.type === 'DELETED') {
        // Collect deletes separately
        pendingDeletes.current.add(event.key);
        pendingKeys.current.delete(event.key);
        pendingDeltaUpdates.current.delete(event.key);
      } else if (event.fields) {
        // Delta update: fields included in event, no GetResourcesByKeys needed
        pendingDeltaUpdates.current.set(event.key, event.fields);
        pendingKeys.current.delete(event.key); // Remove from fallback queue
      } else {
        // Fallback: no fields, need to fetch via GetResourcesByKeys
        pendingKeys.current.add(event.key);
      }
    });

    // Cleanup - unsubscribe event listeners
    // StopWatch is called in the chained operation to avoid race conditions
    return () => {
      unsubscribeSyncProgress();
      unsubscribeSyncComplete();
      unsubscribeResourceUpdate();
      setWatchStatus('disconnected');
    };
    // NOTE: fieldChangeRestartKey is included to re-run this effect when
    // the selectedFields effect determines a resync is needed (SQLStore mode).
    // debouncedPreviewField is NOT a dependency - preview field changes are handled
    // by a separate effect to avoid losing event subscriptions on early return.
  }, [watch, gvk, contexts, fieldChangeRestartKey]);

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

  // Handle selectedFields changes - may restart watch if backend requires resync
  // In SQLStore mode, adding new fields requires a watch restart because
  // the original objects are not kept in memory for re-extraction.
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

    console.log('useResourceData: selectedFields changed, checking if resync needed');

    // Update backend selectedFields and check if resync is needed
    SetSelectedFields(selectedFields)
      .then(async (result) => {
        // If backend indicates resync is needed (SQLStore mode with new fields),
        // trigger a full watch restart by updating fieldChangeRestartKey.
        // This causes the main watch effect to re-run with proper event subscriptions.
        if (result && result.needsResync) {
          // Calculate which fields are new (for skeleton UI)
          // Use ref to avoid stale closure issues
          const newFields = selectedFields.filter(f => !extractedFieldsRef.current.has(f));
          console.log('useResourceData: backend requires resync for new fields:', newFields);

          // Add new fields to loadingFields for skeleton UI
          if (newFields.length > 0) {
            setLoadingFields(prev => {
              const next = new Set(prev);
              for (const f of newFields) {
                next.add(f);
              }
              return next;
            });
          }

          // Mark this as field-change restart (skip full loading spinner)
          isFieldChangeRestartRef.current = true;

          // Trigger watch restart (this will clear loadingFields when data arrives)
          setFieldChangeRestartKey(prev => prev + 1);
          return;
        }

        // No resync needed (FieldStore mode or no new fields) - just re-fetch existing keys
        console.log('useResourceData: no resync needed, queuing keys for re-fetch');
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
  }, [watch, gvk, contexts, watchStatus, selectedFieldsKey, selectedFields]);

  // Handle debouncedPreviewField changes without restarting watch.
  // This is separate from the main watch effect to avoid losing event subscriptions.
  useEffect(() => {
    if (!watch || !gvk || watchStatus !== 'connected') return;
    if (!debouncedPreviewField) return;
    if (extractedFieldsRef.current.has(debouncedPreviewField)) return;

    console.log('useResourceData: preview field change, showing skeleton:', debouncedPreviewField);
    setLoadingFields(prev => new Set([...prev, debouncedPreviewField]));

    // Build combined fields: selected + preview
    const allFields = [...selectedFields];
    if (!allFields.includes(debouncedPreviewField)) {
      allFields.push(debouncedPreviewField);
    }

    SetSelectedFields(allFields)
      .then(async (result) => {
        // Track the preview field as extracted
        extractedFieldsRef.current.add(debouncedPreviewField);
        setExtractedFields(new Set(extractedFieldsRef.current));

        if (result && result.needsResync) {
          console.log('useResourceData: preview field requires resync');
          isFieldChangeRestartRef.current = true;
          setFieldChangeRestartKey(prev => prev + 1);
        } else {
          // Queue keys for re-fetch to get preview field values
          try {
            const allKeys = await GetAllResourceKeys();
            if (allKeys && allKeys.length > 0) {
              for (const key of allKeys) {
                pendingKeys.current.add(key);
              }
            }
          } catch (err) {
            console.error('useResourceData: preview field re-fetch failed:', err);
          }
        }

        setLoadingFields(prev => {
          const next = new Set(prev);
          next.delete(debouncedPreviewField);
          return next;
        });
      })
      .catch((err) => {
        console.error('useResourceData: preview SetSelectedFields failed:', err);
        setLoadingFields(prev => {
          const next = new Set(prev);
          next.delete(debouncedPreviewField);
          return next;
        });
      });
  }, [watch, gvk, watchStatus, debouncedPreviewField, selectedFields]);

  // Batch processor: apply delta updates and fetch remaining resources
  // Delta updates (with fields in event) are applied directly without GetResourcesByKeys
  // Fallback keys (no fields) are fetched via GetResourcesByKeys
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

      // Collect delta updates (resources with fields from events - no fetch needed)
      const deltaUpdates = new Map(pendingDeltaUpdates.current);
      pendingDeltaUpdates.current.clear();

      // Take only a chunk of fallback keys to avoid overwhelming the system
      const allPendingKeys = Array.from(pendingKeys.current);
      const keysToFetch = allPendingKeys.slice(0, FETCH_CHUNK_SIZE);
      const keysToDelete = Array.from(pendingDeletes.current);

      // Debug log to see if flush is being called with data
      if (deltaUpdates.size > 0 || allPendingKeys.length > 0 || keysToDelete.length > 0) {
        console.log(`useResourceData flush: delta=${deltaUpdates.size}, fallback=${allPendingKeys.length}, toFetch=${keysToFetch.length}, toDelete=${keysToDelete.length}`);
      }

      // Remove fetched keys from pending (keep remaining for next batch)
      for (const key of keysToFetch) {
        pendingKeys.current.delete(key);
      }
      pendingDeletes.current.clear();

      // Nothing to do
      if (deltaUpdates.size === 0 && keysToFetch.length === 0 && keysToDelete.length === 0) {
        return;
      }

      // Log progress for large batches
      if (allPendingKeys.length > FETCH_CHUNK_SIZE) {
        console.log(`useResourceData: fetching chunk ${keysToFetch.length}/${allPendingKeys.length} fallback keys`);
      }

      // Fetch resources for fallback keys (no fields in event)
      let fetchedResources: Record<string, unknown>[] = [];
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

        // Apply delta updates (ADDED/MODIFIED with fields from events - no fetch needed)
        for (const [key, resource] of deltaUpdates) {
          const prevResource = dataMap.get(key);

          // Track changes for MODIFIED
          if (prevResource) {
            const changedPaths = diffFields(prevResource, resource);
            for (const columnId of changedPaths) {
              changes.push({ rowId: key, columnId, timestamp: now });
            }
          }

          dataMap.set(key, resource);
        }

        // Apply fetched resources (fallback ADDED/MODIFIED)
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
        // Clear loadingFields - field data has arrived
        setLoadingFields(new Set());
      }
    };

    const timer = setInterval(flush, batchInterval);
    return () => clearInterval(timer);
    // IMPORTANT: Dependencies must match the main watch effect to keep watchGen in sync.
    // If the main effect re-runs and increments watchGen, this effect must also re-run
    // to capture the new watchGen, otherwise flush will skip due to mismatch.
  }, [watch, batchInterval, gvk, contexts, fieldChangeRestartKey]);

  // Manual refresh - restarts the watch
  const refresh = useCallback(() => {
    if (!gvk || contexts.length === 0) return;

    const watchGen = ++watchGenRef.current;
    setLoading(true);
    setError(null);
    setData([]);
    pendingKeys.current.clear();
    pendingDeltaUpdates.current.clear();
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
    loadingFields,
    extractedFields,
  };
}
