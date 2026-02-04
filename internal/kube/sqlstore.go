package kube

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLStore stores extracted field values for resources in SQLite.
// This replaces FieldStore to reduce memory usage by moving data to disk.
type SQLStore struct {
	mu sync.RWMutex
	db *sql.DB

	// Prepared statements for common operations
	stmtUpsert *sql.Stmt
	stmtDelete *sql.Stmt
	stmtGet    *sql.Stmt
}

// NewSQLStore creates a new SQLStore instance.
// dbPath can be ":memory:" for in-memory database, or a file path for persistent storage.
func NewSQLStore(dbPath string) (*SQLStore, error) {
	// Enable optimizations via connection string
	if dbPath == ":memory:" {
		// For in-memory, use shared cache to allow connection pooling
		dbPath = "file::memory:?mode=memory&cache=shared&_busy_timeout=5000"
	} else {
		// For file-based, enable WAL mode and other optimizations
		dbPath = fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_busy_timeout=5000", dbPath)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	// Set connection pool limits to prevent excessive connections
	// SQLite works best with limited concurrency due to file locking
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Create table
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS resources (
		key TEXT PRIMARY KEY,
		context TEXT NOT NULL,
		namespace TEXT NOT NULL,
		name TEXT NOT NULL,
		data BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_context ON resources(context);
	CREATE INDEX IF NOT EXISTS idx_namespace ON resources(namespace);
	`
	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create resources table: %w", err)
	}

	// Prepare statements
	stmtUpsert, err := db.Prepare(`
		INSERT INTO resources (key, context, namespace, name, data)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET data = excluded.data
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to prepare upsert statement: %w", err)
	}

	stmtDelete, err := db.Prepare(`DELETE FROM resources WHERE key = ?`)
	if err != nil {
		stmtUpsert.Close()
		db.Close()
		return nil, fmt.Errorf("failed to prepare delete statement: %w", err)
	}

	stmtGet, err := db.Prepare(`SELECT data FROM resources WHERE key = ?`)
	if err != nil {
		stmtUpsert.Close()
		stmtDelete.Close()
		db.Close()
		return nil, fmt.Errorf("failed to prepare get statement: %w", err)
	}

	return &SQLStore{
		db:         db,
		stmtUpsert: stmtUpsert,
		stmtDelete: stmtDelete,
		stmtGet:    stmtGet,
	}, nil
}

// Upsert inserts or updates a resource in the store.
// key format: "context/namespace/name"
// data: JSON-encoded field values
func (s *SQLStore) Upsert(key, context, namespace, name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.stmtUpsert.Exec(key, context, namespace, name, data)
	if err != nil {
		return fmt.Errorf("failed to upsert resource %s: %w", key, err)
	}
	return nil
}

// Delete removes a resource from the store.
func (s *SQLStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.stmtDelete.Exec(key)
	if err != nil {
		return fmt.Errorf("failed to delete resource %s: %w", key, err)
	}
	return nil
}

// Get retrieves a single resource by key.
func (s *SQLStore) Get(key string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data []byte
	err := s.stmtGet.QueryRow(key).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get resource %s: %w", key, err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resource %s: %w", key, err)
	}
	return result, nil
}

// GetByKeys retrieves multiple resources by their keys.
// Returns a slice of maps containing the stored field values.
// Each map includes "_context" field extracted from the key.
func (s *SQLStore) GetByKeys(keys []string) ([]map[string]any, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build query with placeholders
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		placeholders[i] = "?"
		args[i] = key
	}

	query := fmt.Sprintf(
		"SELECT key, data FROM resources WHERE key IN (%s)",
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query resources: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0, len(keys))
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			log.Printf("Warning: failed to scan row: %v", err)
			continue
		}

		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			log.Printf("Warning: failed to unmarshal data for %s: %v", key, err)
			continue
		}

		// Extract context from key (format: "context/namespace/name")
		parts := strings.SplitN(key, "/", 2)
		if len(parts) > 0 {
			obj["_context"] = parts[0]
		}

		result = append(result, obj)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return result, nil
}

// DeleteByContext removes all resources for a specific context.
func (s *SQLStore) DeleteByContext(context string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM resources WHERE context = ?", context)
	if err != nil {
		return fmt.Errorf("failed to delete resources for context %s: %w", context, err)
	}
	return nil
}

// List returns all resource keys in the store.
func (s *SQLStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT key FROM resources")
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			log.Printf("Warning: failed to scan key: %v", err)
			continue
		}
		keys = append(keys, key)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return keys, nil
}

// Count returns the number of resources in the store.
func (s *SQLStore) Count() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM resources").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count resources: %w", err)
	}
	return count, nil
}

// Clear removes all resources from the store.
func (s *SQLStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM resources")
	if err != nil {
		return fmt.Errorf("failed to clear resources: %w", err)
	}
	return nil
}

// isValidSortField validates that the sort field only contains safe characters
// to prevent SQL injection. Allows alphanumeric, dots, underscores, and hyphens.
func isValidSortField(field string) bool {
	if field == "" {
		return true
	}
	for _, r := range field {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// GetRange returns rows for the given range (0-indexed) with optional sorting.
// sortField: JSON field path to sort by (e.g., "metadata.name", "metadata.creationTimestamp")
// sortDesc: true for descending order
// Returns rows with _context field populated.
func (s *SQLStore) GetRange(start, end int, sortField string, sortDesc bool) ([]map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := end - start
	if limit <= 0 {
		return nil, nil
	}

	// Build ORDER BY clause
	// For JSON fields, we use json_extract
	orderBy := "name ASC" // default sort
	if sortField != "" && isValidSortField(sortField) {
		// Convert dot notation to SQLite json_extract path
		// e.g., "metadata.creationTimestamp" -> json_extract(data, '$.metadata.creationTimestamp')
		jsonPath := "$." + sortField
		direction := "ASC"
		if sortDesc {
			direction = "DESC"
		}
		orderBy = fmt.Sprintf("json_extract(data, '%s') %s", jsonPath, direction)
	}

	query := fmt.Sprintf(`
		SELECT key, context, data FROM resources
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, orderBy)

	rows, err := s.db.Query(query, limit, start)
	if err != nil {
		return nil, fmt.Errorf("failed to query range: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0, limit)
	for rows.Next() {
		var key, context string
		var data []byte
		if err := rows.Scan(&key, &context, &data); err != nil {
			log.Printf("Warning: failed to scan row: %v", err)
			continue
		}

		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			log.Printf("Warning: failed to unmarshal data for %s: %v", key, err)
			continue
		}

		obj["_context"] = context
		obj["_key"] = key
		result = append(result, obj)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return result, nil
}

// Close closes the database connection and releases all resources.
func (s *SQLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stmtUpsert != nil {
		s.stmtUpsert.Close()
	}
	if s.stmtDelete != nil {
		s.stmtDelete.Close()
	}
	if s.stmtGet != nil {
		s.stmtGet.Close()
	}

	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
