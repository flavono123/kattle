/**
 * Resource utilities for real-time updates
 *
 * Provides helper functions for:
 * - Generating stable resource keys
 * - Diffing resource fields for change tracking
 */

// Types for resource events
export type ResourceEventType = 'ADDED' | 'MODIFIED' | 'DELETED';

// Delta update model: event includes optional fields for direct application.
// For ADDED/MODIFIED: fields contains the reconstructed object.
// For DELETED: fields is undefined.
export interface ResourceEventMeta {
  type: ResourceEventType;
  /** Unique key: "context/namespace/name" */
  key: string;
  /** Reconstructed object for ADDED/MODIFIED events (delta update) */
  fields?: Record<string, unknown>;
}

/**
 * Generate a unique key for a Kubernetes resource
 * Format: {context}/{namespace}/{name}
 *
 * This key is stable across updates and can be used for:
 * - TanStack Table row identity
 * - Map-based lookups for batch updates
 */
export function getResourceKey(resource: any): string {
  const context = resource._context || '';
  const namespace = resource.metadata?.namespace || '';
  const name = resource.metadata?.name || '';
  return `${context}/${namespace}/${name}`;
}

// Types for cell change tracking
export interface CellChange {
  rowId: string;      // resource key (context/namespace/name)
  columnId: string;   // field path (e.g., "status.phase")
  timestamp: number;  // when the change was detected
}

/**
 * Compare two objects and return the paths of changed leaf values
 *
 * @param prev Previous object state
 * @param next New object state
 * @param path Current path (used for recursion)
 * @returns Array of dot-separated paths where values differ
 */
export function diffFields(prev: any, next: any, path: string[] = []): string[] {
  const changes: string[] = [];

  // Handle null/undefined cases
  if (prev === next) return changes;
  if (prev == null && next == null) return changes;
  if (prev == null || next == null) {
    // One is null, the other isn't - this path changed
    if (path.length > 0) {
      return [path.join('.')];
    }
    return changes;
  }

  // Handle non-object types (leaf values)
  if (typeof prev !== 'object' || typeof next !== 'object') {
    if (prev !== next && path.length > 0) {
      return [path.join('.')];
    }
    return changes;
  }

  // Handle arrays
  if (Array.isArray(prev) || Array.isArray(next)) {
    // For arrays, compare stringified versions for simplicity
    // (we don't track individual array element changes)
    if (JSON.stringify(prev) !== JSON.stringify(next) && path.length > 0) {
      return [path.join('.')];
    }
    return changes;
  }

  // Handle objects - recurse into properties
  const allKeys = new Set([...Object.keys(prev), ...Object.keys(next)]);

  for (const key of allKeys) {
    const prevVal = prev[key];
    const nextVal = next[key];
    const currentPath = [...path, key];

    changes.push(...diffFields(prevVal, nextVal, currentPath));
  }

  return changes;
}
