package storage

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CapStore quota limits.
const (
	CapsMaxTablesPerCap = 10
	CapsMaxRowsPerTable = 10000
	CapsMaxCellBytes    = 64 * 1024 // 64 KB
)

// ColumnDef describes a column in a cap table.
type ColumnDef struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // TEXT, INTEGER, REAL, BLOB
	Unique     bool   `json:"unique,omitempty"`
	PrimaryKey bool   `json:"primary_key,omitempty"`
	NotNull    bool   `json:"not_null,omitempty"`
}

// TableInfo describes a cap table.
type TableInfo struct {
	Name      string `json:"name"`
	RowCount  int    `json:"row_count"`
	CreatedAt string `json:"created_at"`
}

var validCapName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var validColTypes = map[string]bool{"TEXT": true, "INTEGER": true, "REAL": true, "BLOB": true}

// blockedSQL keywords must not appear in WHERE clauses outside of
// quoted string literals. Keyword-in-literal (e.g. `body LIKE '%DELETE%'`)
// is legit; keyword-as-statement (e.g. `UNION SELECT`, `; DROP`) is not.
// stringLiteralPattern strips `'...'` content before keyword-matching so
// legit text searches pass while statement-stacking and cross-cap
// information-leak subqueries (SELECT ... FROM pragma_table_info)
// remain blocked.
var blockedSQLPattern = regexp.MustCompile(`(?i)\b(UNION|ATTACH|DROP|ALTER|PRAGMA|CREATE|INSERT|UPDATE|DELETE|GRANT|REVOKE|SELECT)\b|;`)
var stringLiteralPattern = regexp.MustCompile(`'[^']*'`)

func validateCapName(name string) error {
	return ValidateCapName(name)
}

// ValidateCapName validates a cap or table name against the shared regex.
// Must match [a-z][a-z0-9_]{0,63} — lowercase letter start, up to 64 chars total.
func ValidateCapName(name string) error {
	if !validCapName.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match [a-z][a-z0-9_]{0,63}", name)
	}
	return nil
}

func sanitizeWhere(where string) error {
	stripped := stringLiteralPattern.ReplaceAllString(where, "''")
	if blockedSQLPattern.MatchString(stripped) {
		return fmt.Errorf("WHERE clause contains blocked SQL keyword")
	}
	return nil
}

func resolveTableName(capName, tableName string) string {
	return "cap_" + capName + "__" + tableName
}

// OpenCapsDB opens (or creates) the caps.db database.
// Uses DELETE journal mode instead of WAL to prevent stale reads when
// concurrent daemon goroutines recycle the pooled connection.
func (s *Store) OpenCapsDB(dir string) error {
	path := dir + "/caps.db"
	db, err := openSQLite(path)
	if err != nil {
		return fmt.Errorf("open cap_store: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		db.Close()
		return fmt.Errorf("set journal_mode: %w", err)
	}
	s.capStoreDB = db
	return s.createCapStoreSchema()
}

func (s *Store) createCapStoreSchema() error {
	_, err := s.capStoreDB.Exec(`CREATE TABLE IF NOT EXISTS cap_store_meta (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		cap_name   TEXT NOT NULL,
		table_name TEXT NOT NULL,
		full_name  TEXT NOT NULL UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(cap_name, table_name)
	)`)
	return err
}

// CloseCapsDB closes the capabilities database.
func (s *Store) CloseCapsDB() {
	if s.capStoreDB != nil {
		s.capStoreDB.Close()
		s.capStoreDB = nil
	}
}

// CapsReady returns true if the cap database is open.
func (s *Store) CapsReady() bool {
	return s.capStoreDB != nil
}

// GetCapTableDDL returns the CREATE TABLE statements for all tables belonging to a cap.
func (s *Store) GetCapTableDDL(capName string) (string, error) {
	if s.capStoreDB == nil {
		return "", nil
	}
	rows, err := s.capStoreDB.Query(
		`SELECT sm.sql
		   FROM cap_store_meta m
		   JOIN sqlite_master sm ON sm.name = m.full_name
		  WHERE m.cap_name = ?
		    AND sm.type = 'table'
		    AND sm.sql IS NOT NULL
		  ORDER BY m.full_name`,
		capName,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var sql string
		if err := rows.Scan(&sql); err != nil {
			continue
		}
		stmts = append(stmts, sql+";")
	}
	return strings.Join(stmts, "\n"), nil
}

// CapsCreateTable creates a namespaced table for a cap.
func (s *Store) CapsCreateTable(capName, tableName string, columns []ColumnDef) error {
	if err := validateCapName(capName); err != nil {
		return err
	}
	if err := validateCapName(tableName); err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("columns required")
	}
	for _, c := range columns {
		if err := validateCapName(c.Name); err != nil {
			return fmt.Errorf("column %s: %w", c.Name, err)
		}
		if !validColTypes[strings.ToUpper(c.Type)] {
			return fmt.Errorf("invalid column type %q (allowed: TEXT, INTEGER, REAL, BLOB)", c.Type)
		}
	}

	// Quota check: max tables per cap
	var count int
	if err := s.capStoreDB.QueryRow(`SELECT COUNT(*) FROM cap_store_meta WHERE cap_name = ?`, capName).Scan(&count); err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if count >= CapsMaxTablesPerCap {
		return fmt.Errorf("quota exceeded: max %d tables per cap", CapsMaxTablesPerCap)
	}

	fullName := resolveTableName(capName, tableName)

	// Build CREATE TABLE with validated column names and types
	var colDefs []string
	colDefs = append(colDefs, "id INTEGER PRIMARY KEY AUTOINCREMENT")
	for _, c := range columns {
		def := c.Name + " " + strings.ToUpper(c.Type)
		if c.PrimaryKey {
			def += " UNIQUE NOT NULL"
		} else if c.Unique {
			def += " UNIQUE"
		}
		if c.NotNull && !c.PrimaryKey {
			def += " NOT NULL"
		}
		colDefs = append(colDefs, def)
	}
	colDefs = append(colDefs, "created_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	colDefs = append(colDefs, "updated_at DATETIME DEFAULT CURRENT_TIMESTAMP")

	ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", fullName, strings.Join(colDefs, ", "))
	if _, err := s.capStoreDB.Exec(ddl); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	_, err := s.capStoreDB.Exec(
		`INSERT OR IGNORE INTO cap_store_meta (cap_name, table_name, full_name) VALUES (?, ?, ?)`,
		capName, tableName, fullName,
	)
	return err
}

// capsUniqueColumn returns the first UNIQUE or PK column in fullName
// that also appears in data. Uses PRAGMA index_list + index_info.
func (s *Store) capsUniqueColumn(fullName string, data map[string]any) string {
	idxRows, err := s.capStoreDB.Query(fmt.Sprintf("PRAGMA index_list(%s)", fullName))
	if err != nil {
		return ""
	}
	var uniqueIndexes []string
	for idxRows.Next() {
		var seq int
		var name string
		var unique int
		var origin, partial string
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			continue
		}
		if unique != 0 {
			uniqueIndexes = append(uniqueIndexes, name)
		}
	}
	idxRows.Close()

	for _, idxName := range uniqueIndexes {
		infoRows, err := s.capStoreDB.Query(fmt.Sprintf("PRAGMA index_info(%s)", idxName))
		if err != nil {
			continue
		}
		for infoRows.Next() {
			var seqno, cid int
			var colName string
			if err := infoRows.Scan(&seqno, &cid, &colName); err != nil {
				continue
			}
			if _, ok := data[colName]; ok {
				infoRows.Close()
				return colName
			}
		}
		infoRows.Close()
	}
	return ""
}

// CapsUpsert inserts a new row or, when data["id"] is set, replaces the row with that id.
// When the table has UNIQUE or PrimaryKey columns and data contains a matching value,
// the upsert auto-detects the conflict target for natural-key dedup.
// Returns the row ID.
func (s *Store) CapsUpsert(capName, tableName string, data map[string]any) (int64, error) {
	if err := validateCapName(capName); err != nil {
		return 0, err
	}
	if err := validateCapName(tableName); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("data required")
	}

	fullName := resolveTableName(capName, tableName)
	if !s.capStoreTableExists(fullName) {
		return 0, fmt.Errorf("table %s.%s does not exist", capName, tableName)
	}

	// Cell size check
	for k, v := range data {
		if s, ok := v.(string); ok && len(s) > CapsMaxCellBytes {
			return 0, fmt.Errorf("cell %q exceeds %d byte limit", k, CapsMaxCellBytes)
		}
	}

	// Row quota check
	var rowCount int
	if err := s.capStoreDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", fullName)).Scan(&rowCount); err != nil {
		return 0, fmt.Errorf("row quota check: %w", err)
	}
	if _, hasID := data["id"]; !hasID && rowCount >= CapsMaxRowsPerTable {
		return 0, fmt.Errorf("quota exceeded: max %d rows per table", CapsMaxRowsPerTable)
	}

	// Build parameterized INSERT ... ON CONFLICT DO UPDATE.
	conflictCol := "id"
	if _, hasID := data["id"]; !hasID {
		if uc := s.capsUniqueColumn(fullName, data); uc != "" {
			conflictCol = uc
		}
	}

	var cols []string
	var placeholders []string
	var vals []any
	var updateClauses []string
	for k, v := range data {
		if err := validateCapName(k); err != nil {
			return 0, fmt.Errorf("column name %s: %w", k, err)
		}
		cols = append(cols, k)
		placeholders = append(placeholders, "?")
		vals = append(vals, v)
		if k != "id" && k != conflictCol {
			updateClauses = append(updateClauses, fmt.Sprintf("%s = excluded.%s", k, k))
		}
	}
	// Always update updated_at on both insert and conflict-update paths.
	cols = append(cols, "updated_at")
	placeholders = append(placeholders, "?")
	vals = append(vals, time.Now().UTC().Format(time.RFC3339))
	updateClauses = append(updateClauses, "updated_at = excluded.updated_at")

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(%s) DO UPDATE SET %s",
		fullName, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		conflictCol,
		strings.Join(updateClauses, ", "),
	)

	result, err := s.capStoreDB.Exec(query, vals...)
	if err != nil {
		return 0, fmt.Errorf("upsert: %w", err)
	}

	// On UPDATE (id conflict matched an existing row), LastInsertId does not
	// reflect the updated row. When the caller supplied an explicit id, return
	// it directly; otherwise use LastInsertId for the fresh-insert path.
	if idVal, ok := data["id"]; ok && idVal != nil {
		switch v := idVal.(type) {
		case int64:
			return v, nil
		case int:
			return int64(v), nil
		case int32:
			return int64(v), nil
		case float64:
			return int64(v), nil
		case float32:
			return int64(v), nil
		default:
			return 0, fmt.Errorf("upsert: id has unsupported type %T", v)
		}
	}
	return result.LastInsertId()
}

// CapsQuery selects rows with an optional WHERE clause using parameterized args.
func (s *Store) CapsQuery(capName, tableName, where string, args []any, limit int) ([]map[string]any, error) {
	if err := validateCapName(capName); err != nil {
		return nil, err
	}
	if err := validateCapName(tableName); err != nil {
		return nil, err
	}

	fullName := resolveTableName(capName, tableName)
	if !s.capStoreTableExists(fullName) {
		return nil, fmt.Errorf("table %s.%s does not exist", capName, tableName)
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	query := fmt.Sprintf("SELECT * FROM %s", fullName)
	if where != "" {
		if err := sanitizeWhere(where); err != nil {
			return nil, err
		}
		query += " WHERE " + where
	}
	query += fmt.Sprintf(" ORDER BY id DESC LIMIT %d", limit)

	rows, err := s.capStoreDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}
	return results, nil
}

// QueryResult carries paginated rows plus metadata so callers can loop
// through arbitrarily large result sets without blowing the MCP
// response-size limit in a single call.
type QueryResult struct {
	Rows       []map[string]any `json:"rows"`
	Count      int              `json:"count"`
	Total      int              `json:"total"`
	HasMore    bool             `json:"has_more"`
	NextOffset int              `json:"next_offset"`
}

// CapsQueryPaged is the pagination-aware sibling of CapsQuery.
// Behaviour matches CapsQuery for rows, and additionally exposes
// Total (unpaginated count), HasMore, and NextOffset. Offset<0 is
// clamped to 0; limit follows the same 1..1000 range as CapsQuery.
func (s *Store) CapsQueryPaged(capName, tableName, where string, args []any, limit, offset int) (*QueryResult, error) {
	if err := validateCapName(capName); err != nil {
		return nil, err
	}
	if err := validateCapName(tableName); err != nil {
		return nil, err
	}

	fullName := resolveTableName(capName, tableName)
	if !s.capStoreTableExists(fullName) {
		return nil, fmt.Errorf("table %s.%s does not exist", capName, tableName)
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	whereClause := ""
	if where != "" {
		if err := sanitizeWhere(where); err != nil {
			return nil, err
		}
		whereClause = " WHERE " + where
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", fullName, whereClause)
	if err := s.capStoreDB.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	query := fmt.Sprintf("SELECT * FROM %s%s ORDER BY id DESC LIMIT %d OFFSET %d", fullName, whereClause, limit, offset)
	rows, err := s.capStoreDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	hasMore := offset+len(results) < total
	nextOffset := offset + len(results)
	return &QueryResult{
		Rows:       results,
		Count:      len(results),
		Total:      total,
		HasMore:    hasMore,
		NextOffset: nextOffset,
	}, nil
}
func (s *Store) CapsDelete(capName, tableName, where string, args []any) (int64, error) {
	if err := validateCapName(capName); err != nil {
		return 0, err
	}
	if err := validateCapName(tableName); err != nil {
		return 0, err
	}
	if where == "" {
		return 0, fmt.Errorf("WHERE clause required for delete (use 1=1 for all)")
	}
	if err := sanitizeWhere(where); err != nil {
		return 0, err
	}

	fullName := resolveTableName(capName, tableName)
	if !s.capStoreTableExists(fullName) {
		return 0, fmt.Errorf("table %s.%s does not exist", capName, tableName)
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE %s", fullName, where)
	result, err := s.capStoreDB.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	return result.RowsAffected()
}

// CapsListTables returns all tables for a cap.
func (s *Store) CapsListTables(capName string) ([]TableInfo, error) {
	if capName != "" {
		if err := validateCapName(capName); err != nil {
			return nil, err
		}
	}

	query := `SELECT table_name, full_name, created_at FROM cap_store_meta`
	var args []any
	if capName != "" {
		query += ` WHERE cap_name = ?`
		args = append(args, capName)
	}
	query += ` ORDER BY created_at`

	rows, err := s.capStoreDB.Query(query, args...)
	if err != nil {
		return nil, err
	}

	type metaRow struct {
		tName, fName, createdAt string
	}
	var metas []metaRow
	for rows.Next() {
		var m metaRow
		if err := rows.Scan(&m.tName, &m.fName, &m.createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list tables iteration: %w", err)
	}
	rows.Close()

	var tables []TableInfo
	for _, m := range metas {
		var rowCount int
		if err := s.capStoreDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", m.fName)).Scan(&rowCount); err != nil {
			return nil, fmt.Errorf("count rows for %s: %w", m.tName, err)
		}
		tables = append(tables, TableInfo{
			Name:      m.tName,
			RowCount:  rowCount,
			CreatedAt: m.createdAt,
		})
	}
	return tables, nil
}

func (s *Store) capStoreTableExists(fullName string) bool {
	var count int
	s.capStoreDB.QueryRow(`SELECT COUNT(*) FROM cap_store_meta WHERE full_name = ?`, fullName).Scan(&count)
	return count > 0
}

var validOrderPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(?:\s+(?:ASC|DESC))?(?:\s*,\s*[a-zA-Z_][a-zA-Z0-9_]*(?:\s+(?:ASC|DESC))?)*$`)

func sanitizeOrderClause(order string) error {
	if !validOrderPattern.MatchString(order) {
		return fmt.Errorf("invalid ORDER BY: %q (allowed: column [ASC|DESC] [, ...])", order)
	}
	return nil
}

func (s *Store) CapsClaimAndRead(capName, tableName, where, order string, args []any, set map[string]any, returning []string) (map[string]any, error) {
	if err := validateCapName(capName); err != nil { return nil, err }
	if err := validateCapName(tableName); err != nil { return nil, err }
	fullName := resolveTableName(capName, tableName)
	if !s.capStoreTableExists(fullName) { return nil, fmt.Errorf("table %s.%s does not exist", capName, tableName) }
	if where == "" { return nil, fmt.Errorf("WHERE clause required for claim_and_read") }
	if err := sanitizeWhere(where); err != nil { return nil, err }
	if order == "" { order = "id ASC" }
	if err := sanitizeOrderClause(order); err != nil { return nil, err }
	if len(returning) == 0 { returning = []string{"*"} }
	for _, col := range returning {
		if col == "*" { continue }
		if err := validateCapName(col); err != nil { return nil, fmt.Errorf("returning column %s: %w", col, err) }
	}
	if len(set) == 0 { return nil, fmt.Errorf("set columns required for claim_and_read") }
	var setClauses []string
	var setVals []any
	for k, v := range set {
		if err := validateCapName(k); err != nil { return nil, fmt.Errorf("set column %s: %w", k, err) }
		setClauses = append(setClauses, k+" = ?")
		setVals = append(setVals, v)
	}
	returningStr := strings.Join(returning, ", ")
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = (SELECT id FROM %s WHERE %s ORDER BY %s LIMIT 1) RETURNING %s", fullName, strings.Join(setClauses, ", "), fullName, where, order, returningStr)
	queryVals := append(setVals, args...)
	rows, err := s.capStoreDB.Query(query, queryVals...)
	if err != nil { return nil, fmt.Errorf("claim_and_read: %w", err) }
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil { return nil, err }
	if !rows.Next() { return nil, rows.Err() }
	values := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range values { ptrs[i] = &values[i] }
	if err := rows.Scan(ptrs...); err != nil { return nil, err }
	row := make(map[string]any, len(columns))
	for i, col := range columns { row[col] = values[i] }
	return row, nil
}
