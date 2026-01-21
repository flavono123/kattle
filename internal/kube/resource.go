package kube

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Event metrics for debugging memory leaks
var (
	eventsEmitted  atomic.Int64
	eventsDropped  atomic.Int64
	syncProcessed  atomic.Int64 // items processed via onSync callback during initial list
	trySendCalled  atomic.Int64 // items that went through trySend (isInInitialList=false)
)

// GetEventMetrics returns the current event metrics (emitted, dropped, syncProcessed, trySendCalled)
func GetEventMetrics() (emitted, dropped, synced, trySent int64) {
	return eventsEmitted.Load(), eventsDropped.Load(), syncProcessed.Load(), trySendCalled.Load()
}

// ResetEventMetrics resets the event counters to zero
func ResetEventMetrics() {
	eventsEmitted.Store(0)
	eventsDropped.Store(0)
	syncProcessed.Store(0)
	trySendCalled.Store(0)
}

// EventType represents the type of watch event
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
)

// WatchEvent represents a watch event with type and object
type WatchEvent struct {
	Type EventType
	Obj  *unstructured.Unstructured
}

// Deprecated: use WatchEvent instead
type emitMsg = WatchEvent

type ResourceController struct {
	contextName string // optional, for GUI multi-context support
	client      dynamic.Interface
	gvr         schema.GroupVersionResource
	store       cache.Store       // may be KeyOnlyStore (memory-efficient) or default store
	keyStore    *KeyOnlyStore     // non-nil when using memory-efficient mode
	emitCh      chan emitMsg
	doneCh      chan struct{} // signals that controller is closed (for event consumers)
	closed      atomic.Bool   // guards trySend to prevent sends after close

	// nameCache stores object names by key to avoid race conditions during sorting.
	// Updated synchronously by informer handlers, read by Objects().
	nameCache   map[string]string
	nameCacheMu sync.RWMutex
}

// NewResourceController creates a controller for the current context (legacy, kept for TUI compatibility)
func NewResourceController(gvr schema.GroupVersionResource) *ResourceController {
	return NewResourceControllerForContext("", gvr)
}

// NewResourceControllerForContext creates a controller for the specified context
// If contextName is empty, uses the current context
func NewResourceControllerForContext(contextName string, gvr schema.GroupVersionResource) *ResourceController {
	client, err := DynamicClientForContext(contextName)
	if err != nil {
		panic(err)
	}

	if contextName == "" {
		contextName, _ = GetCurrentContext()
	}

	return &ResourceController{
		contextName: contextName,
		client:      client,
		gvr:         gvr,
		emitCh:      make(chan emitMsg, 256),
		doneCh:      make(chan struct{}),
		nameCache:   make(map[string]string),
	}
}

// Context returns the context name this controller is connected to
func (i *ResourceController) Context() string {
	return i.contextName
}

func (i *ResourceController) Objects() []*unstructured.Unstructured {
	// Get keys from store first to avoid reading from object maps during sort.
	// This prevents race conditions with concurrent informer updates.
	keys := i.store.ListKeys()

	// Sort keys using cached names (avoid reading from objects)
	i.nameCacheMu.RLock()
	sort.Slice(keys, func(a, b int) bool {
		return i.nameCache[keys[a]] < i.nameCache[keys[b]]
	})
	i.nameCacheMu.RUnlock()

	// Retrieve objects by sorted keys
	objs := make([]*unstructured.Unstructured, 0, len(keys))
	for _, key := range keys {
		item, exists, err := i.store.GetByKey(key)
		if err != nil || !exists {
			continue
		}
		if u, ok := item.(*unstructured.Unstructured); ok {
			objs = append(objs, u)
		}
	}

	return objs
}

// SyncCallback is called synchronously for each object during initial sync.
// This runs in the informer's goroutine, so there's no race condition.
type SyncCallback func(eventType EventType, obj *unstructured.Unstructured)

// InformWithKeyOnlyStore starts the informer with a memory-efficient KeyOnlyStore.
// This is the recommended method for GUI usage where FieldStore handles data storage.
// Objects are NOT retained in memory after event handlers process them.
//
// The onSync callback is called synchronously for each object during initial list sync.
// This guarantees all initial objects are processed without race conditions.
// After sync completes, ongoing events are emitted via WatchEvents() channel.
func (i *ResourceController) InformWithKeyOnlyStore(onSync SyncCallback) (chan struct{}, error) {
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return i.client.Resource(i.gvr).Namespace("").List(context.Background(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return i.client.Resource(i.gvr).Namespace("").Watch(context.Background(), options)
		},
	}

	// Use KeyOnlyStore instead of default store to avoid retaining full objects
	keyStore := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)
	i.keyStore = keyStore
	i.store = keyStore

	// Create DeltaFIFO with KeyOnlyStore as KnownObjects
	fifo := cache.NewDeltaFIFOWithOptions(cache.DeltaFIFOOptions{
		KnownObjects:          keyStore,
		EmitDeltaTypeReplaced: true,
	})

	// Process function handles events and updates KeyOnlyStore
	process := func(obj interface{}, isInInitialList bool) error {
		deltas, ok := obj.(cache.Deltas)
		if !ok {
			return fmt.Errorf("expected Deltas, got %T", obj)
		}

		for _, delta := range deltas {
			u, ok := delta.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			key, _ := cache.MetaNamespaceKeyFunc(u)

			switch delta.Type {
			case cache.Added, cache.Replaced, cache.Sync:
				// Update KeyOnlyStore (only tracks keys)
				keyStore.Add(u)
				// Cache name for sorting
				i.nameCacheMu.Lock()
				i.nameCache[key] = u.GetName()
				i.nameCacheMu.Unlock()

				if isInInitialList && onSync != nil {
					// During initial sync: call callback synchronously (no race condition)
					syncProcessed.Add(1)
					onSync(EventAdded, u)
				} else {
					// After sync: emit via channel (may drop if buffer full)
					trySendCalled.Add(1)
					i.trySend(emitMsg{Type: EventAdded, Obj: u})
				}

			case cache.Updated:
				keyStore.Update(u)
				i.nameCacheMu.Lock()
				i.nameCache[key] = u.GetName()
				i.nameCacheMu.Unlock()

				if isInInitialList && onSync != nil {
					syncProcessed.Add(1)
					onSync(EventModified, u)
				} else {
					trySendCalled.Add(1)
					i.trySend(emitMsg{Type: EventModified, Obj: u})
				}

			case cache.Deleted:
				keyStore.Delete(u)
				i.nameCacheMu.Lock()
				delete(i.nameCache, key)
				i.nameCacheMu.Unlock()

				if isInInitialList && onSync != nil {
					syncProcessed.Add(1)
					onSync(EventDeleted, u)
				} else {
					trySendCalled.Add(1)
					i.trySend(emitMsg{Type: EventDeleted, Obj: u})
				}
			}
		}
		return nil
	}

	cfg := &cache.Config{
		Queue:            fifo,
		ListerWatcher:    lw,
		ObjectType:       &unstructured.Unstructured{},
		FullResyncPeriod: 0, // No resync
		Process:          process,
	}

	controller := cache.New(cfg)
	stop := make(chan struct{})
	go controller.Run(stop)

	if !cache.WaitForCacheSync(stop, controller.HasSynced) {
		close(stop)
		return nil, fmt.Errorf("failed to sync cache")
	}

	return stop, nil
}

// Inform starts the informer with default cache.Store (legacy, for TUI compatibility).
// Use InformWithKeyOnlyStore for memory-efficient GUI usage.
func (i *ResourceController) Inform() (chan struct{}, error) {
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return i.client.Resource(i.gvr).Namespace("").List(context.Background(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return i.client.Resource(i.gvr).Namespace("").Watch(context.Background(), options)
		},
	}

	options := cache.InformerOptions{
		ListerWatcher: lw,
		ObjectType:    &unstructured.Unstructured{},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return
				}
				// Cache the name for race-free sorting in Objects()
				key, _ := cache.MetaNamespaceKeyFunc(u)
				i.nameCacheMu.Lock()
				i.nameCache[key] = u.GetName()
				i.nameCacheMu.Unlock()

				i.trySend(emitMsg{Type: EventAdded, Obj: u})
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				n, ok := newObj.(*unstructured.Unstructured)
				if !ok {
					return
				}
				// Update cached name for this key, since the object reference may change
				key, _ := cache.MetaNamespaceKeyFunc(n)
				i.nameCacheMu.Lock()
				i.nameCache[key] = n.GetName()
				i.nameCacheMu.Unlock()

				i.trySend(emitMsg{Type: EventModified, Obj: n})
			},
			DeleteFunc: func(obj interface{}) {
				var d *unstructured.Unstructured
				var key string

				// Handle DeletedFinalStateUnknown wrapper
				if deleted, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					d, ok = deleted.Obj.(*unstructured.Unstructured)
					if !ok {
						return
					}
					key = deleted.Key
				} else {
					var ok bool
					d, ok = obj.(*unstructured.Unstructured)
					if !ok {
						return
					}
					key, _ = cache.MetaNamespaceKeyFunc(d)
				}

				// Remove from name cache
				i.nameCacheMu.Lock()
				delete(i.nameCache, key)
				i.nameCacheMu.Unlock()

				i.trySend(emitMsg{Type: EventDeleted, Obj: d})
			},
		},
	}
	store, controller := cache.NewInformerWithOptions(
		options,
	)
	i.store = store

	stop := make(chan struct{})
	go controller.Run(stop)

	if !cache.WaitForCacheSync(stop, controller.HasSynced) {
		close(stop)
		return nil, fmt.Errorf("failed to sync cache")
	}

	return stop, nil
}

// WatchEvents returns a read-only channel of watch events
func (i *ResourceController) WatchEvents() <-chan WatchEvent {
	return i.emitCh
}

// Deprecated: use WatchEvents instead
func (i *ResourceController) EventEmitted() <-chan emitMsg {
	return i.emitCh
}

// trySend attempts to send an event to the channel without blocking.
// If the channel buffer is full or controller is closed, the event is dropped.
// This is safe for Kubernetes watch events since they send full object state.
func (i *ResourceController) trySend(msg emitMsg) {
	if i.closed.Load() {
		return
	}
	select {
	case i.emitCh <- msg:
		eventsEmitted.Add(1)
	default:
		// buffer full, drop event (next event will have latest state)
		dropped := eventsDropped.Add(1)
		if dropped%100 == 1 { // Log every 100 drops to avoid spam
			log.Printf("[WARN] Event dropped (total: %d, buffer full for %s/%s)",
				dropped, i.contextName, i.gvr.Resource)
		}
	}
}

// Done returns a channel that is closed when the controller is closed.
// Use this to detect when to stop consuming events from WatchEvents().
func (i *ResourceController) Done() <-chan struct{} {
	return i.doneCh
}

// Close marks the controller as closed and signals consumers to stop.
// After Close is called, new events will be dropped.
// It is safe to call Close multiple times (subsequent calls are no-ops).
func (i *ResourceController) Close() {
	// Use atomic.Bool to ensure we only close doneCh once
	if i.closed.CompareAndSwap(false, true) {
		close(i.doneCh)
	}
}
