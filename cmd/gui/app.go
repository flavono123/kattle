package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/flavono123/kattle/internal/kube"
	"github.com/flavono123/kattle/internal/store"
)

// useSQLStore determines whether to use SQLStore instead of FieldStore.
// Set KATTLE_USE_SQLSTORE=1 environment variable to enable.
// When enabled, frontend should also use windowed mode for memory optimization.
var useSQLStore = os.Getenv("KATTLE_USE_SQLSTORE") == "1"

// IsWindowedModeEnabled returns whether windowed mode should be used.
// This is tied to SQLStore - if SQLStore is enabled, windowed mode should be too.
func (a *App) IsWindowedModeEnabled() bool {
	return useSQLStore
}

// logMemoryStats logs current goroutine count and event metrics for debugging
func logMemoryStats(label string) {
	emitted, dropped, synced, trySent := kube.GetEventMetrics()
	log.Printf("[DEBUG] %s: goroutines=%d, events_emitted=%d, events_dropped=%d, sync_processed=%d, trySend_called=%d",
		label, goruntime.NumGoroutine(), emitted, dropped, synced, trySent)
}

// App struct
type App struct {
	ctx           context.Context
	favoriteStore *store.Store

	// Watch state
	watchMu         sync.RWMutex
	controllers     []*watchController
	stopChs         []chan struct{}
	watchDone       chan struct{}
	fieldStore      *kube.FieldStore    // key: "context/namespace/name" → value: extracted fields
	sqlStore        *kube.SQLStore      // SQLite-based store (enabled via KATTLE_USE_SQLSTORE=1)
	extractedFields map[string]struct{} // fields that were extracted during initial sync (for SQLStore mode)
}

// SetSelectedFieldsResult is the return type for SetSelectedFields
type SetSelectedFieldsResult struct {
	// NeedsResync is true if the watch needs to be restarted to fetch newly selected fields.
	// This happens in SQLStore mode when new fields are added that weren't extracted during initial sync.
	NeedsResync bool `json:"needsResync"`
	// Extracting is true when async field extraction is in progress (cache miss).
	// Frontend should show skeleton until "fields:ready" event is received.
	Extracting bool `json:"extracting"`
}

// watchController wraps a ResourceController with context info
type watchController struct {
	contextName string
	controller  *kube.ResourceController
}

// cleanupOldDBFiles removes stale kattle database files from previous runs/crashes
func cleanupOldDBFiles(tmpDir string) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return
	}

	currentPid := os.Getpid()
	for _, entry := range entries {
		name := entry.Name()
		// Match pattern: kattle-<pid>.db or kattle-<pid>.db-*
		if !strings.HasPrefix(name, "kattle-") {
			continue
		}

		// Extract PID from filename
		var pid int
		if _, err := fmt.Sscanf(name, "kattle-%d.db", &pid); err != nil {
			continue
		}

		// Skip current process's db file
		if pid == currentPid {
			continue
		}

		// Remove old db file and its WAL/SHM files
		filePath := filepath.Join(tmpDir, name)
		if err := os.Remove(filePath); err == nil {
			log.Printf("Cleaned up old db file: %s", name)
		}
	}
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Detect dev mode from wails environment
	env := runtime.Environment(ctx)
	devMode := env.BuildType == "dev"

	s, err := store.NewStore(store.StoreOptions{DevMode: devMode})
	if err != nil {
		log.Printf("failed to create favorite store: %v", err)
	} else {
		a.favoriteStore = s
		if err := a.favoriteStore.Load(); err != nil {
			log.Printf("failed to load favorite views: %v", err)
		}
		log.Printf("favorite store initialized (dev=%v)", devMode)
	}

	// Initialize SQLStore if feature flag is enabled
	// Uses file-based SQLite to move data OFF the Go heap
	if useSQLStore {
		tmpDir := os.TempDir()

		// Cleanup old kattle db files from previous crashes
		cleanupOldDBFiles(tmpDir)

		// Create temp file for SQLite database
		dbPath := filepath.Join(tmpDir, fmt.Sprintf("kattle-%d.db", os.Getpid()))

		sqlStore, err := kube.NewSQLStore(dbPath)
		if err != nil {
			log.Printf("failed to create SQLStore: %v", err)
		} else {
			a.sqlStore = sqlStore
			log.Printf("SQLStore initialized (file: %s)", dbPath)
		}
	}
}

// shutdown is called when the app is closing.
// Cleans up resources including active watches.
func (a *App) shutdown(ctx context.Context) {
	log.Printf("App shutting down, cleaning up resources...")
	a.StopWatch()

	// Close SQLStore
	if a.sqlStore != nil {
		if err := a.sqlStore.Close(); err != nil {
			log.Printf("Warning: failed to close SQLStore: %v", err)
		}
		a.sqlStore = nil
	}

	log.Printf("App shutdown complete")
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}

// ListContexts returns all available Kubernetes contexts
func (a *App) ListContexts() ([]string, error) {
	return kube.ListContexts()
}

// RefreshContexts invalidates the kubeconfig cache and reloads contexts
func (a *App) RefreshContexts() ([]string, error) {
	kube.InvalidateKubeconfigCache()
	return kube.ListContexts()
}

// GetCurrentContext returns the current active Kubernetes context
func (a *App) GetCurrentContext() (string, error) {
	return kube.GetCurrentContext()
}

// ContextConnectionResult represents the result of connecting to a context
type ContextConnectionResult struct {
	Context string `json:"context"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ConnectToContexts attempts to create clients for the specified contexts
// Returns a list of results indicating success or failure for each context
func (a *App) ConnectToContexts(contexts []string) []ContextConnectionResult {
	results := make([]ContextConnectionResult, 0, len(contexts))

	for _, contextName := range contexts {
		result := ContextConnectionResult{
			Context: contextName,
			Success: false,
		}

		// Try to create a client for this context
		// This validates the context and ensures we can connect
		discoveryClient, err := kube.DiscoveryClientForContext(contextName)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		// Actually verify authentication by making a lightweight API call
		_, err = discoveryClient.ServerVersion()
		if err != nil {
			// Check if error is related to tsh authentication
			if strings.Contains(err.Error(), "tsh") {
				// Try tsh kube login
				attempted, loginErr := kube.TryTshKubeLogin(contextName)
				if attempted {
					if loginErr != nil {
						result.Error = fmt.Sprintf("tsh kube login failed: %v", loginErr)
						results = append(results, result)
						continue
					}

					// Login succeeded, invalidate cache and retry
					kube.InvalidateClientCache(contextName)

					// Recreate client and retry
					discoveryClient, err = kube.DiscoveryClientForContext(contextName)
					if err != nil {
						result.Error = err.Error()
						results = append(results, result)
						continue
					}

					_, err = discoveryClient.ServerVersion()
					if err != nil {
						result.Error = err.Error()
						results = append(results, result)
						continue
					}

					// Success after retry
					result.Success = true
					results = append(results, result)
					continue
				}
			}

			// Original error (not relogin or not using tsh)
			result.Error = err.Error()
		} else {
			result.Success = true
		}

		results = append(results, result)
	}

	return results
}

// MultiClusterGVK represents a Kubernetes resource (Group/Version/Kind) with context availability
type MultiClusterGVK struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Kind       string   `json:"kind"`
	ShortNames []string `json:"shortNames"` // Short names from API (e.g., "po" for Pod, "deploy" for Deployment)
	Contexts   []string `json:"contexts"`   // Contexts where this GVK is available
	AllCount   int      `json:"allCount"`   // Total number of contexts
}

// GetGVKs retrieves all unique GVKs from the specified contexts
// Returns a merged and deduplicated list of GVKs with context availability info
func (a *App) GetGVKs(contexts []string) []MultiClusterGVK {
	// Map to track unique GVKs: key = "group/version/kind"
	resourceMap := make(map[string]*MultiClusterGVK)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Process contexts in parallel
	for _, contextName := range contexts {
		wg.Add(1)
		go func(ctx string) {
			defer wg.Done()

			gvkInfos, err := kube.GetGVKInfosForContext(ctx)
			if err != nil {
				// Skip contexts that fail
				return
			}

			for _, info := range gvkInfos {
				// Create unique key using GVK (no GVR conversion needed)
				key := fmt.Sprintf("%s/%s/%s", info.Group, info.Version, info.Kind)

				// Thread-safe map update
				mu.Lock()
				if existing, exists := resourceMap[key]; exists {
					// Add context to existing GVK
					existing.Contexts = append(existing.Contexts, ctx)
					// ShortNames should be consistent across contexts, but take the first non-empty one
					if len(existing.ShortNames) == 0 && len(info.ShortNames) > 0 {
						existing.ShortNames = info.ShortNames
					}
				} else {
					// Create new GVK entry
					resourceMap[key] = &MultiClusterGVK{
						Group:      info.Group,
						Version:    info.Version,
						Kind:       info.Kind,
						ShortNames: info.ShortNames,
						Contexts:   []string{ctx},
						AllCount:   len(contexts),
					}
				}
				mu.Unlock()
			}
		}(contextName)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Convert map to slice
	results := make([]MultiClusterGVK, 0, len(resourceMap))
	for _, info := range resourceMap {
		results = append(results, *info)
	}

	return results
}

// TreeNode represents a node in the navigation tree (frontend format)
type TreeNode struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`     // e.g., "string", "[]Pod", "map[string]"
	FullPath []string    `json:"fullPath"` // for selection/search
	Level    int         `json:"level"`
	Children []*TreeNode `json:"children"`
	// Note: Expanded and Selected state are managed in the frontend
}

// GetNodeTree retrieves the node tree for a given GVK and contexts
// Returns a tree structure representing the schema + actual data
func (a *App) GetNodeTree(gvk MultiClusterGVK, contexts []string) ([]*TreeNode, error) {
	// Convert MultiClusterGVK to schema.GroupVersionKind
	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	// 1. Get field tree from schema (use first available context from GVK)
	var fields map[string]*kube.Field
	var err error
	if len(gvk.Contexts) > 0 {
		// Use the first context where this GVK is available
		fields, err = kube.CreateFieldTreeForContext(gvk.Contexts[0], schemaGVK)
	} else {
		fields, err = kube.CreateFieldTree(schemaGVK)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create field tree: %w", err)
	}

	// 2. Create node tree - use FieldStore if watches are active (memory-efficient)
	// CRITICAL: Take a snapshot under lock to avoid race conditions with StopWatch
	a.watchMu.RLock()
	fs := a.fieldStore
	a.watchMu.RUnlock()

	var nodes map[string]*kube.Node
	if fs != nil && fs.Count() > 0 {
		// Use FieldStore's structure metadata for tree building
		nodes = kube.CreateNodeTreeFromStore(fields, fs, []string{})
	} else {
		// No active watch or empty FieldStore - return schema-only tree
		// IMPORTANT: Do NOT fall back to getResourcesWithCleanup() as it uses
		// memory-heavy Inform() method. The frontend will retry when data is available.
		nodes = kube.CreateNodeTree(fields, nil, []string{})
	}

	// 3. Convert to frontend format (remove UI state, convert to array)
	return convertNodeTree(nodes), nil
}

// GetDefaultSelectedPaths returns the default fields to select for a GVK.
// For CRDs, this returns paths from additionalPrinterColumns.
// For built-in resources, this uses Table API printer columns when available.
func (a *App) GetDefaultSelectedPaths(gvk MultiClusterGVK, contexts []string) [][]string {
	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	// Try each context until we get printer columns
	for _, contextName := range contexts {
		paths, err := kube.GetPrinterColumnsForContext(contextName, schemaGVK)
		if err == nil && len(paths) > 0 {
			return paths
		}
	}

	// Also try with contexts from GVK if different
	for _, contextName := range gvk.Contexts {
		paths, err := kube.GetPrinterColumnsForContext(contextName, schemaGVK)
		if err == nil && len(paths) > 0 {
			return paths
		}
	}

	return nil
}

// ResourceEventMeta represents a watch event with optional delta data.
// - When KATTLE_USE_SQLSTORE=0 (default): includes Fields for direct frontend update (delta model)
// - When KATTLE_USE_SQLSTORE=1: Fields is omitted, frontend uses Pull Model via GetResourcesByKeys
// For DELETED events: Fields is always omitted.
type ResourceEventMeta struct {
	Type   string                 `json:"type"`             // "ADDED", "MODIFIED", "DELETED"
	Key    string                 `json:"key"`              // "context/namespace/name" unique identifier
	Fields map[string]interface{} `json:"fields,omitempty"` // Reconstructed object (only when !useSQLStore and not DELETED)
}

// makeResourceKey creates a unique cache key for a resource
func makeResourceKey(context, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", context, namespace, name)
}

// Essential metadata fields to always extract for SQLStore
var essentialFieldPaths = []string{
	"metadata.name",
	"metadata.namespace",
	"metadata.uid",
	"metadata.resourceVersion",
	"metadata.creationTimestamp",
	"metadata.labels",
	"metadata.ownerReferences",
	"metadata.deletionTimestamp",
	"metadata.finalizers",
}

// extractFieldsForSQL extracts essential + selected fields from an unstructured object
// for storage in SQLStore. This avoids FieldStore overhead while maintaining
// the same field extraction logic.
func extractFieldsForSQL(obj *unstructured.Unstructured, selectedFields []string) map[string]any {
	result := make(map[string]any)

	// Build extraction set
	extractPaths := make(map[string]struct{})
	for _, p := range essentialFieldPaths {
		extractPaths[p] = struct{}{}
	}
	for _, p := range selectedFields {
		extractPaths[p] = struct{}{}
	}

	// Track already-extracted wildcard parents to avoid duplicate extraction
	extractedParents := make(map[string]struct{})

	// Extract each field path
	for path := range extractPaths {
		parts := splitPath(path)

		// Check for wildcard (*) in path
		wildcardIdx := -1
		for i, p := range parts {
			if p == "*" {
				wildcardIdx = i
				break
			}
		}

		if wildcardIdx > 0 {
			// Extract parent path up to (but not including) the wildcard
			parentPath := strings.Join(parts[:wildcardIdx], ".")
			if _, done := extractedParents[parentPath]; done {
				continue
			}
			extractedParents[parentPath] = struct{}{}
			if val, found, err := unstructured.NestedFieldCopy(obj.Object, parts[:wildcardIdx]...); err == nil && found {
				setNestedField(result, parentPath, val)
			}
		} else {
			// Normal path without wildcard
			if val, found, err := unstructured.NestedFieldCopy(obj.Object, parts...); err == nil && found {
				setNestedField(result, path, val)
			}
		}
	}

	return result
}

// splitPath splits a dot-separated path into parts
func splitPath(path string) []string {
	return strings.Split(path, ".")
}

// setNestedField sets a value at a nested path in a map
func setNestedField(obj map[string]any, path string, value any) {
	parts := splitPath(path)
	current := obj

	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		if _, ok := current[part]; !ok {
			current[part] = make(map[string]any)
		}
		if next, ok := current[part].(map[string]any); ok {
			current = next
		} else {
			// Can't traverse further, skip
			return
		}
	}

	current[parts[len(parts)-1]] = value
}

// StartWatch starts watching resources for the given GVK across specified contexts
// Watch events are emitted via Wails runtime events ("resource:update")
// selectedFields: field paths to extract (e.g., "status.phase", "spec.replicas")
// Pass selected fields HERE (not via SetSelectedFields) to ensure they're
// available during initial sync for memory-efficient field extraction.
//
// ASYNC BEHAVIOR: This function returns immediately after setting up.
// Initial sync happens in background goroutines for each context.
// When sync completes, "sync:complete" event is emitted with total count.
// Frontend should listen for this event instead of calling GetAllResourceKeys immediately.
func (a *App) StartWatch(gvk MultiClusterGVK, contexts []string, selectedFields []string) error {
	log.Printf("[DEBUG] StartWatch: starting for %s/%s/%s with %d contexts, %d selectedFields",
		gvk.Group, gvk.Version, gvk.Kind, len(contexts), len(selectedFields))

	// Stop any existing watch first
	a.StopWatch()

	a.watchMu.Lock()

	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	a.controllers = make([]*watchController, 0, len(contexts))
	a.stopChs = make([]chan struct{}, 0, len(contexts))
	a.watchDone = make(chan struct{})
	a.fieldStore = kube.NewFieldStore()

	// Set selected fields BEFORE initial sync so they're extracted properly.
	// This is critical for memory optimization - objects are GC'd after extraction.
	if len(selectedFields) > 0 {
		a.fieldStore.SetSelectedFields(selectedFields)
	}

	// Track which fields will be extracted (essential + selected)
	// Used by SetSelectedFields to determine if resync is needed
	a.extractedFields = make(map[string]struct{})
	for _, p := range essentialFieldPaths {
		a.extractedFields[p] = struct{}{}
	}
	for _, p := range selectedFields {
		a.extractedFields[p] = struct{}{}
	}

	// Clear SQLStore if it exists (parallel operation mode)
	if a.sqlStore != nil {
		if err := a.sqlStore.Clear(); err != nil {
			log.Printf("Warning: failed to clear SQLStore: %v", err)
		}
	}

	// Capture store references for goroutines
	fs := a.fieldStore
	ss := a.sqlStore // may be nil if feature flag is disabled
	watchDone := a.watchDone

	a.watchMu.Unlock()

	// Start informers in parallel goroutines for faster initial sync
	var syncWg sync.WaitGroup
	var eventWg sync.WaitGroup
	var mu sync.Mutex // protects controllers and stopChs slices

	// Progress tracking for batch events (reduces event flood from 6000+ to ~60)
	// Use a channel to emit events from a dedicated goroutine (more reliable than emitting from informer callbacks)
	const progressBatchSize = 100
	var progressCount atomic.Int64
	progressCh := make(chan int64, 100) // buffered channel for progress updates
	progressDone := make(chan struct{})

	// Dedicated goroutine for emitting progress events
	// This ensures events are emitted from a consistent goroutine context
	go func() {
		defer close(progressDone)
		var lastEmitted int64
		for count := range progressCh {
			if count-lastEmitted >= progressBatchSize {
				lastEmitted = count
				log.Printf("[DEBUG] Emitting sync:progress with count=%d", count)
				runtime.EventsEmit(a.ctx, "sync:progress", map[string]any{
					"count": count,
				})
			}
		}
	}()

	for _, contextName := range contexts {
		gvr, err := kube.GetGVRForContext(contextName, schemaGVK)
		if err != nil {
			log.Printf("Warning: failed to get GVR for %s in context %s: %v", schemaGVK.Kind, contextName, err)
			continue
		}

		syncWg.Add(1)
		go func(ctx string, gvr schema.GroupVersionResource) {
			defer syncWg.Done()

			controller := kube.NewResourceControllerForContext(ctx, gvr)

			// Synchronous callback for initial sync - called in informer's goroutine.
			// This populates store (SQLStore OR FieldStore) and emits batch progress events.
			// When useSQLStore=true, FieldStore is SKIPPED to reduce Go heap memory.
			onSync := func(eventType kube.EventType, obj *unstructured.Unstructured) {
				key := makeResourceKey(ctx, obj.GetNamespace(), obj.GetName())
				if eventType == kube.EventDeleted {
					if useSQLStore && ss != nil {
						if err := ss.Delete(key); err != nil {
							log.Printf("Warning: SQLStore delete failed for %s: %v", key, err)
						}
					} else {
						fs.Delete(key)
					}
				} else {
					if useSQLStore && ss != nil {
						// Store directly in SQLStore, skip FieldStore entirely
						// Read latest extractedFields under lock so fields added by
						// concurrent SetSelectedFields calls are included.
						a.watchMu.RLock()
						currentFields := make([]string, 0, len(a.extractedFields))
						for f := range a.extractedFields {
							currentFields = append(currentFields, f)
						}
						a.watchMu.RUnlock()
						fields := extractFieldsForSQL(obj, currentFields)
						data, err := json.Marshal(fields)
						if err != nil {
							log.Printf("Warning: failed to marshal fields for %s: %v", key, err)
						} else {
							if err := ss.Upsert(key, ctx, obj.GetNamespace(), obj.GetName(), data); err != nil {
								log.Printf("Warning: SQLStore upsert failed for %s: %v", key, err)
							}
						}
					} else {
						// Legacy: use FieldStore
						fs.Update(key, obj)
					}
				}

				// Send progress update to dedicated emitter goroutine (non-blocking)
				newCount := progressCount.Add(1)
				select {
				case progressCh <- newCount:
				default:
					// Channel full, skip this update (next one will catch up)
				}
			}

			// Start the informer with KeyOnlyStore for memory efficiency.
			// This BLOCKS until initial sync completes (WaitForCacheSync).
			stopCh, err := controller.InformWithKeyOnlyStore(onSync)
			if err != nil {
				log.Printf("Warning: failed to start watch for %s in context %s: %v", schemaGVK.Kind, ctx, err)
				return
			}

			log.Printf("Initial sync complete for context %s, FieldStore count: %d", ctx, fs.Count())

			// Register controller under lock
			mu.Lock()
			a.watchMu.Lock()
			a.controllers = append(a.controllers, &watchController{
				contextName: ctx,
				controller:  controller,
			})
			a.stopChs = append(a.stopChs, stopCh)
			a.watchMu.Unlock()
			mu.Unlock()

			// Start goroutine to forward ONGOING events to frontend (after initial sync).
			eventWg.Add(1)
			go func(ctxName string, ctrl *kube.ResourceController) {
				defer eventWg.Done()
				for {
					select {
					case event := <-ctrl.WatchEvents():
						if event.Obj == nil {
							continue
						}

						key := makeResourceKey(ctxName, event.Obj.GetNamespace(), event.Obj.GetName())

						// Get store references under lock to avoid race with StopWatch
						a.watchMu.RLock()
						currentFs := a.fieldStore
						currentSs := a.sqlStore
						var currentSelectedFields []string
						if useSQLStore && a.extractedFields != nil {
							// Use ALL accumulated extracted fields so ongoing events
							// don't overwrite SQLStore rows with fewer fields than
							// what SetSelectedFields' LIST re-extraction produced.
							currentSelectedFields = make([]string, 0, len(a.extractedFields))
							for f := range a.extractedFields {
								currentSelectedFields = append(currentSelectedFields, f)
							}
						} else if currentFs != nil {
							currentSelectedFields = currentFs.GetSelectedFields()
						}
						a.watchMu.RUnlock()

						// Skip if no store available
						if useSQLStore && currentSs == nil {
							continue
						}
						if !useSQLStore && currentFs == nil {
							continue
						}

						eventMeta := ResourceEventMeta{
							Type: string(event.Type),
							Key:  key,
						}

						if string(event.Type) == "DELETED" {
							if useSQLStore && currentSs != nil {
								if err := currentSs.Delete(key); err != nil {
									log.Printf("Warning: SQLStore delete failed for %s: %v", key, err)
								}
							} else if currentFs != nil {
								currentFs.Delete(key)
							}
							// No fields for DELETED events
						} else {
							if useSQLStore && currentSs != nil {
								// Store directly in SQLStore, skip FieldStore
								fields := extractFieldsForSQL(event.Obj, currentSelectedFields)
								data, err := json.Marshal(fields)
								if err != nil {
									log.Printf("Warning: failed to marshal fields for %s: %v", key, err)
								} else {
									if err := currentSs.Upsert(key, ctxName, event.Obj.GetNamespace(), event.Obj.GetName(), data); err != nil {
										log.Printf("Warning: SQLStore upsert failed for %s: %v", key, err)
									}
								}
							} else if currentFs != nil {
								// Legacy: use FieldStore
								currentFs.Update(key, event.Obj)

								// Include reconstructed object in event (delta update)
								if cachedFields := currentFs.ReconstructObject(key); cachedFields != nil {
									fields := make(map[string]interface{}, len(cachedFields)+1)
									for k, v := range cachedFields {
										fields[k] = v
									}
									parts := strings.SplitN(key, "/", 2)
									if len(parts) > 0 {
										fields["_context"] = parts[0]
									}
									eventMeta.Fields = fields
								}
							}
						}

						runtime.EventsEmit(a.ctx, "resource:update", eventMeta)
					case <-ctrl.Done():
						return
					}
				}
			}(ctx, controller)
		}(contextName, gvr)
	}

	// Background goroutine: wait for all initial syncs, then emit sync:complete
	go func() {
		syncWg.Wait()

		// Close progress channel and wait for emitter to finish
		close(progressCh)
		<-progressDone

		// Get count from the active store
		var count int
		if useSQLStore && ss != nil {
			var err error
			count, err = ss.Count()
			if err != nil {
				log.Printf("Warning: failed to get SQLStore count: %v", err)
			}
			log.Printf("All initial syncs complete, total SQLStore count: %d", count)
		} else {
			count = fs.Count()
			log.Printf("All initial syncs complete, total FieldStore count: %d", count)
		}
		logMemoryStats("StartWatch-SyncComplete")

		// Emit sync:complete event so frontend knows data is ready
		runtime.EventsEmit(a.ctx, "sync:complete", map[string]any{
			"count": count,
		})

		// Force memory release after initial sync
		// This returns unused memory to OS, reducing process footprint
		goruntime.GC()
		debug.FreeOSMemory()
		logMemoryStats("StartWatch-AfterGC")
	}()

	// Background goroutine: wait for all event forwarders to finish
	go func() {
		eventWg.Wait()
		close(watchDone)
	}()

	log.Printf("StartWatch returned (sync in progress in background)")
	return nil
}

// StopWatch stops all active resource watches
func (a *App) StopWatch() {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()

	if len(a.stopChs) == 0 {
		log.Printf("[DEBUG] StopWatch: no active watches to stop")
		return
	}

	log.Printf("[DEBUG] StopWatch: stopping %d watches", len(a.stopChs))

	// Close all stop channels to stop informers
	for _, stopCh := range a.stopChs {
		close(stopCh)
	}

	// Close all controllers to unblock goroutines and release resources
	for _, wc := range a.controllers {
		wc.controller.Close()
	}

	// Wait for event forwarders to finish (with timeout)
	if a.watchDone != nil {
		select {
		case <-a.watchDone:
		case <-time.After(2 * time.Second):
			log.Printf("Warning: watch cleanup timed out")
		}
	}

	a.controllers = nil
	a.stopChs = nil
	a.watchDone = nil

	// Clear field store
	if a.fieldStore != nil {
		a.fieldStore.Clear()
	}
	a.fieldStore = nil

	// Clear SQLStore (but keep it open for next watch)
	if a.sqlStore != nil {
		if err := a.sqlStore.Clear(); err != nil {
			log.Printf("Warning: failed to clear SQLStore: %v", err)
		}
	}

	log.Printf("Stopped all resource watches")
	logMemoryStats("StopWatch")

	// Reset event metrics for next watch cycle
	kube.ResetEventMetrics()
}

// SetSelectedFields updates the fields to extract for table display.
// Called by frontend when user changes column selection.
// Fields should be dot-notation paths like "status.phase", "spec.replicas".
//
// In SQLStore mode, acts as a cache: if requested fields are already extracted,
// returns immediately. On cache miss, performs a LIST from Kubernetes API,
// re-extracts all fields (essential + all selected), and updates SQLStore.
// This avoids full watch restart for new field selections.
func (a *App) SetSelectedFields(fields []string) SetSelectedFieldsResult {
	a.watchMu.Lock()

	fs := a.fieldStore
	if fs != nil {
		fs.SetSelectedFields(fields)
	}

	// In non-SQLStore mode, no re-extraction needed
	if !useSQLStore || a.extractedFields == nil {
		a.watchMu.Unlock()
		return SetSelectedFieldsResult{NeedsResync: false, Extracting: false}
	}

	// Find new fields not yet in extractedFields (cache miss)
	var newFields []string
	for _, field := range fields {
		if _, exists := a.extractedFields[field]; !exists {
			newFields = append(newFields, field)
		}
	}

	// Cache hit: all fields already extracted
	if len(newFields) == 0 {
		a.watchMu.Unlock()
		return SetSelectedFieldsResult{NeedsResync: false, Extracting: false}
	}

	// Update extractedFields with new fields
	for _, f := range newFields {
		a.extractedFields[f] = struct{}{}
	}

	// Build allSelectedFields from extractedFields (minus essentials, since extractFieldsForSQL adds them)
	essentialSet := make(map[string]struct{}, len(essentialFieldPaths))
	for _, p := range essentialFieldPaths {
		essentialSet[p] = struct{}{}
	}
	var allSelectedFields []string
	for f := range a.extractedFields {
		if _, isEssential := essentialSet[f]; !isEssential {
			allSelectedFields = append(allSelectedFields, f)
		}
	}

	// Capture references before unlocking
	controllers := make([]*watchController, len(a.controllers))
	copy(controllers, a.controllers)
	ss := a.sqlStore
	watchDone := a.watchDone // capture for goroutine cancellation

	// Unlock BEFORE LIST calls to avoid blocking the event handler goroutines
	a.watchMu.Unlock()

	if ss == nil {
		return SetSelectedFieldsResult{NeedsResync: false, Extracting: false}
	}

	log.Printf("[DEBUG] SetSelectedFields: cache miss for %d new fields (async): %v", len(newFields), newFields)

	// isCancelled checks if the watch has been stopped since this goroutine started.
	isCancelled := func() bool {
		if watchDone == nil {
			return true
		}
		select {
		case <-watchDone:
			return true
		default:
			return false
		}
	}

	// Async extraction: goroutine waits for controllers (if sync in progress),
	// then LIST + re-extract, emits "fields:ready" when done.
	// Checks watchDone at each step to avoid upserting stale data after StopWatch.
	go func() {
		ctrls := controllers

		// If no controllers yet (sync in progress), wait until at least one is registered.
		if len(ctrls) == 0 {
			log.Printf("[DEBUG] SetSelectedFields goroutine: waiting for controllers...")
			for i := 0; i < 600; i++ { // 30s max (600 * 50ms)
				if isCancelled() {
					log.Printf("[DEBUG] SetSelectedFields goroutine: cancelled while waiting for controllers")
					return
				}
				time.Sleep(50 * time.Millisecond)
				a.watchMu.RLock()
				ctrls = make([]*watchController, len(a.controllers))
				copy(ctrls, a.controllers)
				a.watchMu.RUnlock()
				if len(ctrls) > 0 {
					break
				}
			}
			if len(ctrls) == 0 {
				log.Printf("Warning: SetSelectedFields goroutine timed out waiting for controllers")
				if !isCancelled() {
					runtime.EventsEmit(a.ctx, "fields:ready")
				}
				return
			}
			log.Printf("[DEBUG] SetSelectedFields goroutine: got %d controllers", len(ctrls))
		}

		for _, wc := range ctrls {
			if isCancelled() {
				log.Printf("[DEBUG] SetSelectedFields goroutine: cancelled before LIST for %s", wc.contextName)
				return
			}

			list, err := wc.controller.ListAll()
			if err != nil {
				log.Printf("Warning: ListAll failed for context %s: %v", wc.contextName, err)
				continue
			}

			if isCancelled() {
				log.Printf("[DEBUG] SetSelectedFields goroutine: cancelled after LIST for %s", wc.contextName)
				return
			}

			for i := range list.Items {
				obj := &list.Items[i]
				key := makeResourceKey(wc.contextName, obj.GetNamespace(), obj.GetName())
				extracted := extractFieldsForSQL(obj, allSelectedFields)
				data, err := json.Marshal(extracted)
				if err != nil {
					log.Printf("Warning: failed to marshal fields for %s: %v", key, err)
					continue
				}
				if err := ss.Upsert(key, wc.contextName, obj.GetNamespace(), obj.GetName(), data); err != nil {
					log.Printf("Warning: SQLStore upsert failed for %s: %v", key, err)
				}
			}

			log.Printf("[DEBUG] SetSelectedFields: re-extracted %d resources for context %s (async)", len(list.Items), wc.contextName)
		}

		// Notify frontend only if watch is still active
		if !isCancelled() {
			runtime.EventsEmit(a.ctx, "fields:ready")
		} else {
			log.Printf("[DEBUG] SetSelectedFields goroutine: cancelled, skipping fields:ready emit")
		}
	}()

	// Return immediately with Extracting=true (frontend shows skeleton)
	return SetSelectedFieldsResult{NeedsResync: false, Extracting: true}
}

// GetResourcesByKeys fetches resources by keys (Pull Model)
// - When KATTLE_USE_SQLSTORE=1: queries SQLStore (disk-based)
// - When KATTLE_USE_SQLSTORE=0: queries FieldStore (memory-based, legacy)
// Called by frontend after receiving resource:update events
func (a *App) GetResourcesByKeys(keys []string) []map[string]any {
	log.Printf("[DEBUG] GetResourcesByKeys: called with %d keys, useSQLStore=%v", len(keys), useSQLStore)

	// Get store references under lock to avoid race with StopWatch
	a.watchMu.RLock()
	fs := a.fieldStore
	ss := a.sqlStore
	a.watchMu.RUnlock()

	// Use SQLStore if feature flag is enabled
	if useSQLStore && ss != nil {
		result, err := ss.GetByKeys(keys)
		if err != nil {
			log.Printf("Warning: SQLStore GetByKeys failed: %v", err)
			return make([]map[string]any, 0)
		}
		return result
	}

	// Fallback to FieldStore (legacy behavior)
	result := make([]map[string]any, 0, len(keys))
	if fs == nil {
		log.Printf("[DEBUG] GetResourcesByKeys: fieldStore is nil, returning empty")
		return result
	}

	for _, key := range keys {
		if obj := fs.ReconstructObject(key); obj != nil {
			// Extract context from key (format: "context/namespace/name")
			parts := strings.SplitN(key, "/", 2)
			if len(parts) > 0 {
				obj["_context"] = parts[0]
			}
			result = append(result, obj)
		}
	}
	return result
}

// GetAllResourceKeys returns all resource keys currently stored.
// - When KATTLE_USE_SQLSTORE=1: queries SQLStore
// - When KATTLE_USE_SQLSTORE=0: queries FieldStore
// Called by frontend after StartWatch to get initial batch of keys.
// This is needed because trySend drops events when buffer is full during initial sync.
func (a *App) GetAllResourceKeys() []string {
	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	// Use SQLStore if feature flag is enabled
	if useSQLStore && a.sqlStore != nil {
		keys, err := a.sqlStore.List()
		if err != nil {
			log.Printf("Warning: SQLStore List failed: %v", err)
			return []string{}
		}
		log.Printf("[DEBUG] GetAllResourceKeys (SQLStore): returning %d keys", len(keys))
		return keys
	}

	// Fallback to FieldStore (legacy behavior)
	if a.fieldStore == nil {
		log.Printf("[DEBUG] GetAllResourceKeys: fieldStore is nil, returning empty")
		return []string{}
	}

	keys := a.fieldStore.List()
	log.Printf("[DEBUG] GetAllResourceKeys (FieldStore): returning %d keys", len(keys))
	return keys
}

// GetResourceCount returns the total count of resources.
// Used by frontend for virtualization (to know total scroll height).
func (a *App) GetResourceCount() int {
	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if useSQLStore && a.sqlStore != nil {
		count, err := a.sqlStore.Count()
		if err != nil {
			log.Printf("Warning: SQLStore Count failed: %v", err)
			return 0
		}
		return count
	}

	if a.fieldStore != nil {
		return a.fieldStore.Count()
	}
	return 0
}

// GetResourcesRange returns resources for the given range (0-indexed) with sorting.
// This is the key API for virtualized table - only fetches visible rows.
// sortField: field path to sort by (e.g., "metadata.creationTimestamp")
// sortDesc: true for descending order
func (a *App) GetResourcesRange(start, end int, sortField string, sortDesc bool) []map[string]any {
	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if useSQLStore && a.sqlStore != nil {
		result, err := a.sqlStore.GetRange(start, end, sortField, sortDesc)
		if err != nil {
			log.Printf("Warning: SQLStore GetRange failed: %v", err)
			return make([]map[string]any, 0)
		}
		log.Printf("[DEBUG] GetResourcesRange: returning %d rows (start=%d, end=%d, sort=%s, desc=%v)",
			len(result), start, end, sortField, sortDesc)
		return result
	}

	// Fallback for FieldStore - not efficient but works
	// FieldStore doesn't support range queries, so we load all and slice
	log.Printf("Warning: GetResourcesRange called without SQLStore - falling back to full load")
	if a.fieldStore == nil {
		return make([]map[string]any, 0)
	}

	keys := a.fieldStore.List()
	if start >= len(keys) {
		return make([]map[string]any, 0)
	}
	if end > len(keys) {
		end = len(keys)
	}

	result := make([]map[string]any, 0, end-start)
	for _, key := range keys[start:end] {
		if obj := a.fieldStore.ReconstructObject(key); obj != nil {
			parts := strings.SplitN(key, "/", 2)
			if len(parts) > 0 {
				obj["_context"] = parts[0]
			}
			obj["_key"] = key
			result = append(result, obj)
		}
	}
	return result
}

// GetResourcesRangeFiltered returns resources with filtering, sorting, and pagination.
// This is the main API for windowed mode with full query support.
// paramsJSON: JSON-encoded kube.QueryParams
func (a *App) GetResourcesRangeFiltered(paramsJSON string) ([]map[string]any, error) {
	var params kube.QueryParams
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if !useSQLStore || a.sqlStore == nil {
		return nil, fmt.Errorf("SQLStore not enabled - set KATTLE_USE_SQLSTORE=1")
	}

	result, err := a.sqlStore.GetRangeWithFilters(params)
	if err != nil {
		return nil, fmt.Errorf("GetRangeWithFilters failed: %w", err)
	}

	log.Printf("[DEBUG] GetResourcesRangeFiltered: returning %d rows (search=%q, filters=%d)",
		len(result), params.Search, len(params.Filters))
	return result, nil
}

// GetResourceCountFiltered returns total count with filters applied.
// Used for virtual scrollbar calculation in windowed mode.
// paramsJSON: JSON-encoded kube.QueryParams (only Search and Filters are used)
func (a *App) GetResourceCountFiltered(paramsJSON string) (int, error) {
	var params kube.QueryParams
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return 0, fmt.Errorf("invalid params: %w", err)
	}

	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if !useSQLStore || a.sqlStore == nil {
		return 0, fmt.Errorf("SQLStore not enabled - set KATTLE_USE_SQLSTORE=1")
	}

	count, err := a.sqlStore.CountWithFilters(params)
	if err != nil {
		return 0, fmt.Errorf("CountWithFilters failed: %w", err)
	}

	log.Printf("[DEBUG] GetResourceCountFiltered: %d (search=%q, filters=%d)",
		count, params.Search, len(params.Filters))
	return count, nil
}

// convertNodeTree converts kube.Node map to frontend TreeNode array
func convertNodeTree(nodes map[string]*kube.Node) []*TreeNode {
	result := make([]*TreeNode, 0, len(nodes))

	for name, node := range nodes {
		// Skip apiVersion and kind (TUI also skips these)
		if name == "apiVersion" || name == "kind" {
			continue
		}

		treeNode := &TreeNode{
			Name:     node.Name(),
			Type:     node.Type(),
			FullPath: node.NodeFullPath(), // Use NodeFullPath instead of FullPath to include array indices
			Level:    node.Level(),
			Children: convertNodeTree(node.Children()),
		}

		result = append(result, treeNode)
	}

	// Sort result: * always comes first, then numeric indices (sorted numerically), then alphabetically
	sort.Slice(result, func(i, j int) bool {
		// * always comes first
		if result[i].Name == "*" {
			return true
		}
		if result[j].Name == "*" {
			return false
		}

		// Try to parse as numbers
		numI, errI := strconv.Atoi(result[i].Name)
		numJ, errJ := strconv.Atoi(result[j].Name)

		// Both are numbers: sort numerically
		if errI == nil && errJ == nil {
			return numI < numJ
		}

		// One is a number, one is not: numbers come first (after *)
		if errI == nil {
			return true
		}
		if errJ == nil {
			return false
		}

		// Both are strings: sort alphabetically
		return result[i].Name < result[j].Name
	})

	return result
}

// SaveFile opens a save file dialog and saves the content to the selected file
// Returns the path where the file was saved, or empty string if cancelled
func (a *App) SaveFile(defaultFilename string, content string) (string, error) {
	// Get user's Downloads directory as default location
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	defaultDir := filepath.Join(homeDir, "Downloads")

	// Open save file dialog
	filePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultDirectory: defaultDir,
		DefaultFilename:  defaultFilename,
		Title:            "Save CSV File",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "CSV Files (*.csv)",
				Pattern:     "*.csv",
			},
			{
				DisplayName: "All Files (*.*)",
				Pattern:     "*.*",
			},
		},
	})

	if err != nil {
		return "", fmt.Errorf("failed to open save dialog: %w", err)
	}

	// User cancelled the dialog
	if filePath == "" {
		return "", nil
	}

	// Ensure .csv extension
	if !strings.HasSuffix(filePath, ".csv") {
		filePath += ".csv"
	}

	// Write content to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

// FavoriteView types for frontend binding
type FavoriteViewGVK struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

type FavoriteViewResponse struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	GVK       FavoriteViewGVK `json:"gvk"`
	Fields    [][]string      `json:"fields"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

func favoriteViewToResponse(v *store.FavoriteView) FavoriteViewResponse {
	return FavoriteViewResponse{
		ID:   v.ID,
		Name: v.Name,
		GVK: FavoriteViewGVK{
			Group:   v.GVK.Group,
			Version: v.GVK.Version,
			Kind:    v.GVK.Kind,
		},
		Fields:    v.Fields,
		CreatedAt: v.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: v.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListFavoriteViews returns all favorite views.
func (a *App) ListFavoriteViews() ([]FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	views := a.favoriteStore.ListAll()
	result := make([]FavoriteViewResponse, len(views))
	for i, v := range views {
		result[i] = favoriteViewToResponse(&v)
	}
	return result, nil
}

// GetFavoriteViewsForGVK returns favorite views for a specific GVK.
func (a *App) GetFavoriteViewsForGVK(group, version, kind string) ([]FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	gvk := store.GVKRef{Group: group, Version: version, Kind: kind}
	views := a.favoriteStore.ListByGVK(gvk)
	result := make([]FavoriteViewResponse, len(views))
	for i, v := range views {
		result[i] = favoriteViewToResponse(&v)
	}
	return result, nil
}

// SaveFavoriteView saves current selection as a favorite.
func (a *App) SaveFavoriteView(name, group, version, kind string, fields [][]string) (*FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	gvk := store.GVKRef{Group: group, Version: version, Kind: kind}
	view, err := a.favoriteStore.Create(name, gvk, fields)
	if err != nil {
		return nil, err
	}

	if err := a.favoriteStore.Save(); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}

	result := favoriteViewToResponse(view)
	return &result, nil
}

// DeleteFavoriteView removes a favorite view by ID.
func (a *App) DeleteFavoriteView(id string) error {
	if a.favoriteStore == nil {
		return fmt.Errorf("favorite store not initialized")
	}

	if err := a.favoriteStore.Delete(id); err != nil {
		return err
	}

	return a.favoriteStore.Save()
}

// RenameFavoriteView updates the name of a favorite view.
func (a *App) RenameFavoriteView(id, newName string) (*FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	view, err := a.favoriteStore.Rename(id, newName)
	if err != nil {
		return nil, err
	}

	if err := a.favoriteStore.Save(); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}

	result := favoriteViewToResponse(view)
	return &result, nil
}
