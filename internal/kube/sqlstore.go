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

// FilterOp represents a filter operation for SQL queries.
type FilterOp string

const (
	FilterOpContains   FilterOp = "contains"   // LIKE %value%
	FilterOpEquals     FilterOp = "equals"     // = value
	FilterOpStartsWith FilterOp = "startsWith" // LIKE value%
	FilterOpGt         FilterOp = "gt"         // > value
	FilterOpLt         FilterOp = "lt"         // < value
	FilterOpGte        FilterOp = "gte"        // >= value
	FilterOpLte        FilterOp = "lte"        // <= value
	FilterOpIn         FilterOp = "in"         // IN (values)
)

// Filter represents a single filter condition.
type Filter struct {
	Field string   `json:"field"` // JSON path: "metadata.namespace"
	Op    FilterOp `json:"op"`
	Value any      `json:"value"` // string, number, or []string for "in"
}

// QueryParams contains all query parameters for filtered range queries.
type QueryParams struct {
	Start     int      `json:"start"`
	End       int      `json:"end"`
	SortField string   `json:"sortField"`
	SortDesc  bool     `json:"sortDesc"`
	Filters   []Filter `json:"filters"` // AND combination
	Search    string   `json:"search"`  // Global search term
}

// SQLStore stores extracted field values for resources in SQLite.
// This replaces FieldStore to reduce memory usage by moving data to disk.
type SQLStore struct {
	mu sync.RWMutex
	db *sql.DB

	// Prepared statements for common operations
	stmtUpsert *sql.Stmt
	stmtDelete *sql.Stmt
	stmtGet    *sql.Stmt

	// FTS5 availability (false if SQLite build doesn't support it)
	hasFTS bool
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
	CREATE INDEX IF NOT EXISTS idx_name ON resources(name);
	CREATE INDEX IF NOT EXISTS idx_context_namespace_name ON resources(context, namespace, name);
	`
	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create resources table: %w", err)
	}

	// FTS5 for text search (optional - gracefully degrades if unavailable)
	hasFTS := false
	ftsSQL := `CREATE VIRTUAL TABLE IF NOT EXISTS resources_fts USING fts5(name, namespace, content='', contentless_delete=1)`
	if _, err := db.Exec(ftsSQL); err != nil {
		log.Printf("SQLStore: FTS5 not available, falling back to LIKE search: %v", err)
	} else {
		hasFTS = true
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
		hasFTS:     hasFTS,
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

	// Sync FTS index (contentless: manual insert with rowid from main table)
	if s.hasFTS {
		var rowid int64
		if err := s.db.QueryRow("SELECT rowid FROM resources WHERE key = ?", key).Scan(&rowid); err == nil {
			if _, err := s.db.Exec("INSERT OR REPLACE INTO resources_fts(rowid, name, namespace) VALUES (?, ?, ?)", rowid, name, namespace); err != nil {
				log.Printf("Warning: FTS upsert failed for %s: %v", key, err)
			}
		}
	}

	return nil
}

// Delete removes a resource from the store.
func (s *SQLStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get rowid before delete for FTS cleanup
	var rowid int64
	hasFTSRow := false
	if s.hasFTS {
		if err := s.db.QueryRow("SELECT rowid FROM resources WHERE key = ?", key).Scan(&rowid); err == nil {
			hasFTSRow = true
		}
	}

	_, err := s.stmtDelete.Exec(key)
	if err != nil {
		return fmt.Errorf("failed to delete resource %s: %w", key, err)
	}

	// Remove from FTS index
	if s.hasFTS && hasFTSRow {
		if _, err := s.db.Exec("INSERT INTO resources_fts(resources_fts, rowid, name, namespace) VALUES('delete', ?, '', '')", rowid); err != nil {
			log.Printf("Warning: FTS delete failed for %s: %v", key, err)
		}
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

	// Rebuild FTS index (drop and recreate is faster than row-by-row delete)
	if s.hasFTS {
		if _, err := s.db.Exec("DROP TABLE IF EXISTS resources_fts"); err != nil {
			log.Printf("Warning: FTS drop failed: %v", err)
		}
		if _, err := s.db.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS resources_fts USING fts5(name, namespace, content='', contentless_delete=1)"); err != nil {
			log.Printf("Warning: FTS recreate failed: %v", err)
		}
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

// escapeLike escapes special characters for LIKE queries.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// buildFilterClause builds a SQL WHERE clause for a single filter.
// Returns the clause string and arguments for prepared statement.
func buildFilterClause(f Filter) (string, []any) {
	if !isValidSortField(f.Field) {
		return "", nil
	}

	jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", f.Field)

	switch f.Op {
	case FilterOpContains:
		if strVal, ok := f.Value.(string); ok {
			return fmt.Sprintf("%s LIKE ? ESCAPE '\\'", jsonPath),
				[]any{"%" + escapeLike(strVal) + "%"}
		}
	case FilterOpEquals:
		return fmt.Sprintf("%s = ?", jsonPath), []any{f.Value}
	case FilterOpStartsWith:
		if strVal, ok := f.Value.(string); ok {
			return fmt.Sprintf("%s LIKE ? ESCAPE '\\'", jsonPath),
				[]any{escapeLike(strVal) + "%"}
		}
	case FilterOpGt:
		return fmt.Sprintf("%s > ?", jsonPath), []any{f.Value}
	case FilterOpLt:
		return fmt.Sprintf("%s < ?", jsonPath), []any{f.Value}
	case FilterOpGte:
		return fmt.Sprintf("%s >= ?", jsonPath), []any{f.Value}
	case FilterOpLte:
		return fmt.Sprintf("%s <= ?", jsonPath), []any{f.Value}
	case FilterOpIn:
		// Handle []any from JSON unmarshal
		var values []string
		switch v := f.Value.(type) {
		case []string:
			values = v
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					values = append(values, s)
				}
			}
		}
		if len(values) > 0 {
			placeholders := make([]string, len(values))
			args := make([]any, len(values))
			for i, val := range values {
				placeholders[i] = "?"
				args[i] = val
			}
			return fmt.Sprintf("%s IN (%s)", jsonPath, strings.Join(placeholders, ",")), args
		}
	}
	return "", nil
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

// buildWhereClause builds WHERE clause from QueryParams.
// When useFTS is true and search term is present, uses FTS5 MATCH via rowid subquery
// for faster text search. Falls back to LIKE when FTS5 is not available.
func buildWhereClause(params QueryParams, useFTS bool) (string, []any) {
	var whereClauses []string
	var args []any

	// Global search
	if params.Search != "" {
		if useFTS {
			// FTS5 MATCH via rowid subquery (much faster than LIKE for large datasets)
			whereClauses = append(whereClauses, `rowid IN (SELECT rowid FROM resources_fts WHERE resources_fts MATCH ?)`)
			args = append(args, params.Search)
		} else {
			// Fallback: LIKE search on name, namespace, status.phase
			searchPattern := "%" + escapeLike(params.Search) + "%"
			whereClauses = append(whereClauses, `(
				name LIKE ? ESCAPE '\' OR
				namespace LIKE ? ESCAPE '\' OR
				json_extract(data, '$.status.phase') LIKE ? ESCAPE '\'
			)`)
			args = append(args, searchPattern, searchPattern, searchPattern)
		}
	}

	// Individual filters (AND combination)
	for _, f := range params.Filters {
		clause, filterArgs := buildFilterClause(f)
		if clause != "" {
			whereClauses = append(whereClauses, clause)
			args = append(args, filterArgs...)
		}
	}

	if len(whereClauses) == 0 {
		return "", nil
	}

	return "WHERE " + strings.Join(whereClauses, " AND "), args
}

// buildOrderByClause builds ORDER BY clause from sort parameters.
func buildOrderByClause(sortField string, sortDesc bool) string {
	if sortField == "" || !isValidSortField(sortField) {
		return "name ASC"
	}

	jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", sortField)
	direction := "ASC"
	if sortDesc {
		direction = "DESC"
	}
	return fmt.Sprintf("%s %s", jsonPath, direction)
}

// GetRangeWithFilters returns rows matching filters with pagination and sorting.
// This is the primary method for windowed mode with full query support.
func (s *SQLStore) GetRangeWithFilters(params QueryParams) ([]map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := params.End - params.Start
	if limit <= 0 {
		return nil, nil
	}

	whereSQL, whereArgs := buildWhereClause(params, s.hasFTS)
	orderBy := buildOrderByClause(params.SortField, params.SortDesc)

	query := fmt.Sprintf(`
		SELECT key, context, data FROM resources
		%s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, whereSQL, orderBy)

	// Append LIMIT and OFFSET to args
	args := append(whereArgs, limit, params.Start)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		if s.hasFTS && params.Search != "" {
			// FTS5 MATCH failed (e.g., syntax error in search term) - retry with LIKE
			whereSQL, whereArgs = buildWhereClause(params, false)
			query = fmt.Sprintf("SELECT key, context, data FROM resources %s ORDER BY %s LIMIT ? OFFSET ?", whereSQL, orderBy)
			args = append(whereArgs, limit, params.Start)
			rows, err = s.db.Query(query, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to query range with filters: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to query range with filters: %w", err)
		}
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

// CountWithFilters returns total count matching filters.
// Used for virtual scrollbar calculation in windowed mode.
func (s *SQLStore) CountWithFilters(params QueryParams) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereSQL, whereArgs := buildWhereClause(params, s.hasFTS)

	query := fmt.Sprintf("SELECT COUNT(*) FROM resources %s", whereSQL)

	var count int
	err := s.db.QueryRow(query, whereArgs...).Scan(&count)
	if err != nil {
		if s.hasFTS && params.Search != "" {
			// FTS5 MATCH failed - retry with LIKE
			whereSQL, whereArgs = buildWhereClause(params, false)
			query = fmt.Sprintf("SELECT COUNT(*) FROM resources %s", whereSQL)
			err = s.db.QueryRow(query, whereArgs...).Scan(&count)
			if err != nil {
				return 0, fmt.Errorf("failed to count with filters: %w", err)
			}
		} else {
			return 0, fmt.Errorf("failed to count with filters: %w", err)
		}
	}

	return count, nil
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
