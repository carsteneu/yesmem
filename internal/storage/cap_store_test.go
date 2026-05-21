package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mustOpenCapStore(t *testing.T) *Store {
	t.Helper()
	s := mustOpen(t)
	if err := s.OpenCapsDB(t.TempDir()); err != nil {
		t.Fatalf("open cap_store: %v", err)
	}
	t.Cleanup(func() { s.CloseCapsDB() })
	return s
}

func TestCapsCreateTable(t *testing.T) {
	s := mustOpenCapStore(t)
	err := s.CapsCreateTable("reddit", "posts", []ColumnDef{
		{Name: "title", Type: "TEXT"},
		{Name: "score", Type: "INTEGER"},
		{Name: "url", Type: "TEXT"},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	tables, err := s.CapsListTables("reddit")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}
	if tables[0].Name != "posts" {
		t.Errorf("expected posts, got %s", tables[0].Name)
	}
}

func TestCapsCreateTable_Idempotent(t *testing.T) {
	s := mustOpenCapStore(t)
	cols := []ColumnDef{{Name: "val", Type: "TEXT"}}
	if err := s.CapsCreateTable("test", "data", cols); err != nil {
		t.Fatal(err)
	}
	if err := s.CapsCreateTable("test", "data", cols); err != nil {
		t.Fatalf("second create should be idempotent: %v", err)
	}
}

func TestCapsUpsert_And_Query(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("reddit", "posts", []ColumnDef{
		{Name: "title", Type: "TEXT"},
		{Name: "score", Type: "INTEGER"},
	})

	id, err := s.CapsUpsert("reddit", "posts", map[string]any{
		"title": "Hello World",
		"score": 42,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	rows, err := s.CapsQuery("reddit", "posts", "", nil, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["title"] != "Hello World" {
		t.Errorf("expected Hello World, got %v", rows[0]["title"])
	}
}

// Contract: passing an existing id replaces the row in place.
func TestCapsUpsert_UpdateByID(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("t", "items", []ColumnDef{{Name: "name", Type: "TEXT"}})

	id, err := s.CapsUpsert("t", "items", map[string]any{"name": "original"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := s.CapsUpsert("t", "items", map[string]any{"id": id, "name": "updated"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	rows, err := s.CapsQuery("t", "items", "", nil, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after update-by-id, got %d", len(rows))
	}
	if rows[0]["name"] != "updated" {
		t.Errorf("expected name=updated, got %v", rows[0]["name"])
	}
}

// Contract: upsert does NOT merge by natural keys. Same 'name' twice inserts twice.
// Callers wanting natural-key upsert must query first, then pass the found id.
func TestCapsUpsert_NoNaturalKeyMerge(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("t", "items", []ColumnDef{{Name: "name", Type: "TEXT"}})

	if _, err := s.CapsUpsert("t", "items", map[string]any{"name": "same"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.CapsUpsert("t", "items", map[string]any{"name": "same"}); err != nil {
		t.Fatalf("second: %v", err)
	}

	rows, err := s.CapsQuery("t", "items", "", nil, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows (no natural-key merge), got %d", len(rows))
	}
}

func TestCapsQuery_Where(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{
		{Name: "name", Type: "TEXT"},
		{Name: "value", Type: "INTEGER"},
	})
	s.CapsUpsert("test", "items", map[string]any{"name": "a", "value": 1})
	s.CapsUpsert("test", "items", map[string]any{"name": "b", "value": 2})
	s.CapsUpsert("test", "items", map[string]any{"name": "c", "value": 3})

	rows, err := s.CapsQuery("test", "items", "value > ?", []any{1}, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows where value > 1, got %d", len(rows))
	}
}

func TestCapsDelete(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{
		{Name: "name", Type: "TEXT"},
	})
	s.CapsUpsert("test", "items", map[string]any{"name": "keep"})
	s.CapsUpsert("test", "items", map[string]any{"name": "remove"})

	affected, err := s.CapsDelete("test", "items", "name = ?", []any{"remove"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if affected != 1 {
		t.Errorf("expected 1 affected, got %d", affected)
	}

	rows, _ := s.CapsQuery("test", "items", "", nil, 10)
	if len(rows) != 1 {
		t.Errorf("expected 1 remaining row, got %d", len(rows))
	}
}

func TestCapsDelete_RequiresWhere(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{{Name: "name", Type: "TEXT"}})

	_, err := s.CapsDelete("test", "items", "", nil)
	if err == nil {
		t.Error("delete without WHERE should fail")
	}
}

func TestCapStoreNameValidation(t *testing.T) {
	s := mustOpenCapStore(t)

	tests := []struct {
		name string
		ok   bool
	}{
		{"valid", true},
		{"my_tool", true},
		{"a123", true},
		{"", false},
		{"UPPER", false},
		{"has-dash", false},
		{"has space", false},
		{"1starts_with_num", false},
		{"drop table; --", false},
	}

	for _, tt := range tests {
		err := s.CapsCreateTable(tt.name, "t", []ColumnDef{{Name: "v", Type: "TEXT"}})
		if tt.ok && err != nil {
			t.Errorf("name %q should be valid, got: %v", tt.name, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("name %q should be rejected", tt.name)
		}
	}
}

func TestCapStoreTableQuota(t *testing.T) {
	s := mustOpenCapStore(t)

	for i := 0; i < CapsMaxTablesPerCap; i++ {
		name := "t" + string(rune('a'+i))
		err := s.CapsCreateTable("quotatest", name, []ColumnDef{{Name: "v", Type: "TEXT"}})
		if err != nil {
			t.Fatalf("create table %d: %v", i, err)
		}
	}

	err := s.CapsCreateTable("quotatest", "one_too_many", []ColumnDef{{Name: "v", Type: "TEXT"}})
	if err == nil {
		t.Error("should reject table creation beyond quota")
	}
}

func TestCapStoreCellSizeLimit(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "big", []ColumnDef{{Name: "data", Type: "TEXT"}})

	bigData := make([]byte, CapsMaxCellBytes+1)
	for i := range bigData {
		bigData[i] = 'x'
	}

	_, err := s.CapsUpsert("test", "big", map[string]any{"data": string(bigData)})
	if err == nil {
		t.Error("should reject cell exceeding size limit")
	}
}

func TestCapStoreNonExistentTable(t *testing.T) {
	s := mustOpenCapStore(t)

	_, err := s.CapsUpsert("test", "nonexistent", map[string]any{"v": 1})
	if err == nil {
		t.Error("upsert to nonexistent table should fail")
	}

	_, err = s.CapsQuery("test", "nonexistent", "", nil, 10)
	if err == nil {
		t.Error("query on nonexistent table should fail")
	}

	_, err = s.CapsDelete("test", "nonexistent", "1=1", nil)
	if err == nil {
		t.Error("delete from nonexistent table should fail")
	}
}

func TestCapStoreColumnTypeValidation(t *testing.T) {
	s := mustOpenCapStore(t)

	err := s.CapsCreateTable("test", "badtype", []ColumnDef{{Name: "v", Type: "VARCHAR(255)"}})
	if err == nil {
		t.Error("should reject non-standard column type")
	}

	err = s.CapsCreateTable("test", "goodtype", []ColumnDef{{Name: "v", Type: "text"}})
	if err != nil {
		t.Errorf("lowercase type should be accepted: %v", err)
	}
}

func TestCapsListTables_Empty(t *testing.T) {
	s := mustOpenCapStore(t)

	tables, err := s.CapsListTables("nonexistent")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if tables != nil {
		t.Errorf("expected nil for no tables, got %v", tables)
	}
}

func TestCapStoreWhereSanitizer(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{{Name: "name", Type: "TEXT"}})
	s.CapsUpsert("test", "items", map[string]any{"name": "a"})

	dangerous := []string{
		"1=1 UNION SELECT * FROM cap_store_meta",
		"name = 'x'; DROP TABLE cap_test__items",
		"1=1; ATTACH DATABASE ':memory:' AS pwned",
		"name IN (SELECT name FROM pragma_table_info('cap_store_meta'))",
		"1=1; INSERT INTO cap_store_meta VALUES(99,'x','y','z','now')",
		"1=1; ALTER TABLE cap_test__items ADD COLUMN pwned TEXT",
	}

	for _, w := range dangerous {
		_, err := s.CapsQuery("test", "items", w, nil, 10)
		if err == nil {
			t.Errorf("query with %q should be blocked", w)
		}
		_, err = s.CapsDelete("test", "items", w, nil)
		if err == nil {
			t.Errorf("delete with %q should be blocked", w)
		}
	}

	// Safe WHERE should still work
	rows, err := s.CapsQuery("test", "items", "name = ?", []any{"a"}, 10)
	if err != nil {
		t.Fatalf("safe query failed: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(rows))
	}
}

func TestCapsListTables_Populated(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("test", "items", []ColumnDef{{Name: "name", Type: "TEXT"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.CapsUpsert("test", "items", map[string]any{"name": "a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	tables, err := s.CapsListTables("test")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}
	if tables[0].Name != "items" {
		t.Errorf("expected name=items, got %q", tables[0].Name)
	}
	if tables[0].RowCount != 1 {
		t.Errorf("expected RowCount=1, got %d", tables[0].RowCount)
	}
}

func TestCapsListTables_UnderlyingTableMissing(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("test", "items", []ColumnDef{{Name: "name", Type: "TEXT"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate corruption: drop the real table but leave meta intact.
	if _, err := s.capStoreDB.Exec("DROP TABLE cap_test__items"); err != nil {
		t.Fatalf("drop: %v", err)
	}

	_, err := s.CapsListTables("test")
	if err == nil {
		t.Fatal("expected error when underlying table is missing, got nil")
	}
}

func TestCapStoreNilDB(t *testing.T) {
	s := mustOpen(t)
	if s.CapsReady() {
		t.Error("cap store should not be ready without OpenCapsDB")
	}
}

func TestCapsUpsert_UpdateByID_PreservesCreatedAt(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("t", "items", []ColumnDef{{Name: "name", Type: "TEXT"}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	originalCreated := "2020-01-01 00:00:00"
	id, err := s.CapsUpsert("t", "items", map[string]any{
		"name":       "original",
		"created_at": originalCreated,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := s.CapsUpsert("t", "items", map[string]any{
		"id":   id,
		"name": "updated",
	}); err != nil {
		t.Fatalf("update-by-id: %v", err)
	}

	rows, err := s.CapsQuery("t", "items", "id = ?", []any{id}, 1)
	if err != nil {
		t.Fatalf("query after update: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	gotCreated := ""
	switch v := rows[0]["created_at"].(type) {
	case string:
		gotCreated = v
	case []byte:
		gotCreated = string(v)
	case time.Time:
		gotCreated = v.Format("2006-01-02 15:04:05")
	default:
		t.Fatalf("unexpected created_at type %T: %v", v, v)
	}
	if gotCreated != originalCreated {
		t.Errorf("created_at was reset by upsert-by-id: want %q, got %q", originalCreated, gotCreated)
	}
	if rows[0]["name"] != "updated" {
		t.Errorf("expected name=updated, got %v", rows[0]["name"])
	}
}

func TestCapStoreConcurrent_NoDeadlock(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("cap1", "t1", []ColumnDef{{Name: "v", Type: "INTEGER"}}); err != nil {
		t.Fatalf("create cap1: %v", err)
	}
	if err := s.CapsCreateTable("cap2", "t2", []ColumnDef{{Name: "v", Type: "INTEGER"}}); err != nil {
		t.Fatalf("create cap2: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 2)
	worker := func(capName, tblName string) {
		for i := 0; i < 50; i++ {
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			default:
			}
			if _, err := s.CapsListTables(capName); err != nil {
				done <- err
				return
			}
			if _, err := s.CapsUpsert(capName, tblName, map[string]any{"v": i}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}
	go worker("cap1", "t1")
	go worker("cap2", "t2")

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("goroutine failed (possible deadlock): %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("timeout: goroutines did not complete in 5s — likely deadlock")
		}
	}
}

func TestCapsUpsert_UpdateByID_ReturnsProvidedID(t *testing.T) {
	s := mustOpen(t)
	if err := s.OpenCapsDB(t.TempDir()); err != nil {
		t.Fatalf("OpenCapsDB: %v", err)
	}
	defer s.CloseCapsDB()

	if err := s.CapsCreateTable("t", "items", []ColumnDef{{Name: "name", Type: "TEXT"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id, err := s.CapsUpsert("t", "items", map[string]any{"name": "first"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Update-by-id path: caller supplied id must be returned as-is,
	// regardless of LastInsertId (which does not reflect UPDATE rows).
	gotInt64, err := s.CapsUpsert("t", "items", map[string]any{"id": id, "name": "updated"})
	if err != nil {
		t.Fatalf("update int64: %v", err)
	}
	if gotInt64 != id {
		t.Errorf("int64 path: want id=%d, got %d", id, gotInt64)
	}

	// JSON-decoded ids arrive as float64 — verify the defensive type switch.
	gotFloat, err := s.CapsUpsert("t", "items", map[string]any{"id": float64(id), "name": "updated-f"})
	if err != nil {
		t.Fatalf("update float64: %v", err)
	}
	if gotFloat != id {
		t.Errorf("float64 path: want id=%d, got %d", id, gotFloat)
	}

	// Unsupported id type must error, not silently misbehave.
	if _, err := s.CapsUpsert("t", "items", map[string]any{"id": "not-a-number", "name": "bad"}); err == nil {
		t.Errorf("expected error for string id, got nil")
	}
}

func TestSanitizeWhere_AllowsKeywordsInStringLiterals(t *testing.T) {
	cases := []string{
		"body LIKE '%DELETE%'",
		"action = 'UPDATE'",
		"kind = 'SELECT'",
		"text = 'DROP TABLE x'",
		"name = 'INSERT'",
	}
	for _, w := range cases {
		if err := sanitizeWhere(w); err != nil {
			t.Errorf("want %q to pass, got error: %v", w, err)
		}
	}
}

func TestSanitizeWhere_BlocksStatementStacking(t *testing.T) {
	cases := []string{
		"1=1; DROP TABLE victim",
		"id=1; INSERT INTO foo VALUES (1)",
		"key=? ; SELECT 1",
	}
	for _, w := range cases {
		if err := sanitizeWhere(w); err == nil {
			t.Errorf("want %q to be blocked, got nil error", w)
		}
	}
}

func TestCapsQueryPaged_ReturnsTotalAndHasMore(t *testing.T) {
	s := mustOpenCapStore(t)
	cols := []ColumnDef{{Name: "label", Type: "TEXT"}}
	if err := s.CapsCreateTable("pag", "items", cols); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.CapsUpsert("pag", "items", map[string]any{"label": "x"}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// Page 1: limit=2, offset=0 → 2 rows, total=5, has_more=true, next=2
	r, err := s.CapsQueryPaged("pag", "items", "", nil, 2, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(r.Rows) != 2 {
		t.Errorf("page1 rows: want 2 got %d", len(r.Rows))
	}
	if r.Total != 5 {
		t.Errorf("page1 total: want 5 got %d", r.Total)
	}
	if !r.HasMore {
		t.Errorf("page1 has_more: want true got false")
	}
	if r.NextOffset != 2 {
		t.Errorf("page1 next_offset: want 2 got %d", r.NextOffset)
	}

	// Last page: offset=4 → 1 row, has_more=false
	r, err = s.CapsQueryPaged("pag", "items", "", nil, 2, 4)
	if err != nil {
		t.Fatalf("lastpage: %v", err)
	}
	if len(r.Rows) != 1 {
		t.Errorf("lastpage rows: want 1 got %d", len(r.Rows))
	}
	if r.HasMore {
		t.Errorf("lastpage has_more: want false got true")
	}
}

func TestCapsUpsert_CrossGoroutineReadAfterWrite(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("wal", "kv", []ColumnDef{
		{Name: "key", Type: "TEXT"},
		{Name: "value", Type: "TEXT"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	id, err := s.CapsUpsert("wal", "kv", map[string]any{"key": "offset", "value": "old"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	for i := 0; i < 20; i++ {
		want := fmt.Sprintf("v%d", i)
		wrote := make(chan struct{})

		go func() {
			defer close(wrote)
			if _, err := s.CapsUpsert("wal", "kv", map[string]any{"id": id, "key": "offset", "value": want}); err != nil {
				t.Errorf("upsert iter %d: %v", i, err)
			}
		}()

		<-wrote

		rows, err := s.CapsQuery("wal", "kv", "id = ?", []any{id}, 1)
		if err != nil {
			t.Fatalf("query iter %d: %v", i, err)
		}
		if len(rows) != 1 {
			t.Fatalf("iter %d: expected 1 row, got %d", i, len(rows))
		}
		got, _ := rows[0]["value"].(string)
		if got != want {
			t.Fatalf("iter %d: cross-goroutine read-after-write stale: want %q, got %q", i, want, got)
		}
	}
}

func TestCapsQueryPaged_WithWhereFilter(t *testing.T) {
	s := mustOpenCapStore(t)
	cols := []ColumnDef{{Name: "kind", Type: "TEXT"}}
	if err := s.CapsCreateTable("pag", "tagged", cols); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, k := range []string{"a", "b", "a", "a", "b"} {
		if _, err := s.CapsUpsert("pag", "tagged", map[string]any{"kind": k}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	r, err := s.CapsQueryPaged("pag", "tagged", "kind=?", []any{"a"}, 10, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if r.Total != 3 {
		t.Errorf("total: want 3 got %d", r.Total)
	}
	if len(r.Rows) != 3 {
		t.Errorf("rows: want 3 got %d", len(r.Rows))
	}
	if r.HasMore {
		t.Errorf("has_more: want false got true")
	}
}

func TestCapsCreateTable_UniqueConstraint(t *testing.T) {
	s := mustOpenCapStore(t)
	err := s.CapsCreateTable("tg", "config", []ColumnDef{
		{Name: "key", Type: "TEXT", Unique: true},
		{Name: "value", Type: "TEXT"},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := s.CapsUpsert("tg", "config", map[string]any{"key": "offset", "value": "100"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.CapsUpsert("tg", "config", map[string]any{"key": "offset", "value": "200"}); err != nil {
		t.Fatalf("upsert on unique: %v", err)
	}
	rows, err := s.CapsQuery("tg", "config", "key = ?", []any{"offset"}, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row (deduped), got %d", len(rows))
	}
	if rows[0]["value"] != "200" {
		t.Errorf("want value '200' after upsert, got %v", rows[0]["value"])
	}
}

func TestCapsCreateTable_PrimaryKeyConstraint(t *testing.T) {
	s := mustOpenCapStore(t)
	err := s.CapsCreateTable("tg", "msgs", []ColumnDef{
		{Name: "telegram_id", Type: "INTEGER", PrimaryKey: true},
		{Name: "text", Type: "TEXT"},
		{Name: "processed", Type: "INTEGER", NotNull: true},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := s.CapsUpsert("tg", "msgs", map[string]any{"telegram_id": 42, "text": "hello", "processed": 0}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.CapsUpsert("tg", "msgs", map[string]any{"telegram_id": 42, "text": "updated", "processed": 1}); err != nil {
		t.Fatalf("upsert on pk: %v", err)
	}
	rows, err := s.CapsQuery("tg", "msgs", "telegram_id = ?", []any{42}, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row (pk dedup), got %d", len(rows))
	}
	if rows[0]["text"] != "updated" {
		t.Errorf("want text 'updated', got %v", rows[0]["text"])
	}
}

func TestCapsUpsert_UniqueConflictUpdatesNonKeyColumns(t *testing.T) {
	s := mustOpenCapStore(t)
	err := s.CapsCreateTable("app", "users", []ColumnDef{
		{Name: "email", Type: "TEXT", Unique: true},
		{Name: "name", Type: "TEXT"},
		{Name: "score", Type: "INTEGER"},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := s.CapsUpsert("app", "users", map[string]any{"email": "a@b.com", "name": "Alice", "score": 10}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.CapsUpsert("app", "users", map[string]any{"email": "a@b.com", "name": "Alice Updated", "score": 20}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := s.CapsQuery("app", "users", "email = ?", []any{"a@b.com"}, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if fmt.Sprintf("%v", rows[0]["score"]) != "20" {
		t.Errorf("want score 20, got %v", rows[0]["score"])
	}
	if rows[0]["name"] != "Alice Updated" {
		t.Errorf("want name 'Alice Updated', got %v", rows[0]["name"])
	}
}

func TestGetCapTableDDL_PrefixOverlap(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.CapsCreateTable("reddit", "posts", []ColumnDef{{Name: "title", Type: "TEXT"}}); err != nil {
		t.Fatalf("create reddit.posts: %v", err)
	}
	if err := s.CapsCreateTable("reddit_fetch", "blobs", []ColumnDef{{Name: "body", Type: "TEXT"}}); err != nil {
		t.Fatalf("create reddit_fetch.blobs: %v", err)
	}
	if err := s.CapsCreateTable("reddit_search", "listings", []ColumnDef{{Name: "url", Type: "TEXT"}}); err != nil {
		t.Fatalf("create reddit_search.listings: %v", err)
	}

	ddl, err := s.GetCapTableDDL("reddit")
	if err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if !strings.Contains(ddl, "cap_reddit__posts") {
		t.Errorf("want cap_reddit__posts in DDL, got: %s", ddl)
	}
	for _, foreign := range []string{"cap_reddit_fetch__blobs", "cap_reddit_search__listings"} {
		if strings.Contains(ddl, foreign) {
			t.Errorf("DDL for 'reddit' must NOT include %q from sibling cap, got:\n%s", foreign, ddl)
		}
	}
}

func TestGetCapTableDDL_EmptyCap(t *testing.T) {
	s := mustOpenCapStore(t)
	ddl, err := s.GetCapTableDDL("nonexistent")
	if err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if ddl != "" {
		t.Errorf("want empty DDL for nonexistent cap, got: %s", ddl)
	}
}

func TestCapsClaimAndRead_ClaimsAtomic(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("telegram", "updates", []ColumnDef{
		{Name: "chat_id", Type: "INTEGER"},
		{Name: "sender", Type: "TEXT"},
		{Name: "text", Type: "TEXT"},
		{Name: "processed", Type: "INTEGER"},
	})
	s.CapsUpsert("telegram", "updates", map[string]any{"chat_id": 123, "sender": "Alice", "text": "Hello", "processed": 0})
	s.CapsUpsert("telegram", "updates", map[string]any{"chat_id": 456, "sender": "Bob", "text": "Hi", "processed": 0})
	s.CapsUpsert("telegram", "updates", map[string]any{"chat_id": 789, "sender": "Eve", "text": "Hey", "processed": 0})
	row, err := s.CapsClaimAndRead("telegram", "updates", "processed = 0", "id ASC", nil, map[string]any{"processed": 1}, []string{"id", "chat_id", "sender", "text"})
	if err != nil { t.Fatalf("claim: %v", err) }
	if row == nil { t.Fatal("expected a row, got nil") }
	if row["sender"] != "Alice" { t.Errorf("expected Alice, got %v", row["sender"]) }
	remaining, _ := s.CapsQuery("telegram", "updates", "processed = 0", nil, 10)
	if len(remaining) != 2 { t.Errorf("expected 2 unprocessed remaining, got %d", len(remaining)) }
	// Exhaust
	if _, err := s.CapsClaimAndRead("telegram", "updates", "processed = 0", "id ASC", nil, map[string]any{"processed": 1}, []string{"id"}); err != nil { t.Fatal(err) }
	if _, err := s.CapsClaimAndRead("telegram", "updates", "processed = 0", "id ASC", nil, map[string]any{"processed": 1}, []string{"id"}); err != nil { t.Fatal(err) }
	row4, _ := s.CapsClaimAndRead("telegram", "updates", "processed = 0", "id ASC", nil, map[string]any{"processed": 1}, []string{"id"})
	if row4 != nil { t.Errorf("expected nil for exhausted queue, got %v", row4) }
}
func TestCapsClaimAndRead_NoRows(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{{Name: "processed", Type: "INTEGER"}, {Name: "data", Type: "TEXT"}})
	row, err := s.CapsClaimAndRead("test", "items", "processed = 0", "id ASC", nil, map[string]any{"processed": 1}, []string{"id", "data"})
	if err != nil { t.Fatalf("claim empty: %v", err) }
	if row != nil { t.Errorf("expected nil for empty table, got %v", row) }
}
func TestCapsClaimAndRead_RequiresWhere(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{{Name: "processed", Type: "INTEGER"}})
	_, err := s.CapsClaimAndRead("test", "items", "", "id ASC", nil, map[string]any{"processed": 1}, []string{"id"})
	if err == nil { t.Error("claim without WHERE should fail") }
}
func TestCapsClaimAndRead_RequiresSet(t *testing.T) {
	s := mustOpenCapStore(t)
	s.CapsCreateTable("test", "items", []ColumnDef{{Name: "processed", Type: "INTEGER"}})
	_, err := s.CapsClaimAndRead("test", "items", "processed = 0", "id ASC", nil, nil, []string{"id"})
	if err == nil { t.Error("claim without set columns should fail") }
}
