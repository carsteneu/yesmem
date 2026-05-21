package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/carsteneu/yesmem/internal/storage"
)

// handleCapStore dispatches cap database operations by action.
func (h *Handler) handleCapStore(params map[string]any) Response {
	if !h.store.CapsReady() {
		return errorResponse("cap database not available")
	}

	action, _ := params["action"].(string)
	capName, _ := params["capability"].(string)
	tableName, _ := params["table"].(string)

	if capName == "" {
		return errorResponse("cap name required")
	}

	switch action {
	case "create_table":
		return h.capStoreCreateTable(capName, tableName, params)
	case "upsert":
		return h.capStoreUpsert(capName, tableName, params)
	case "query":
		return h.capStoreQuery(capName, tableName, params)
	case "delete":
		return h.capStoreDelete(capName, tableName, params)
	case "list_tables":
		return h.capStoreListTables(capName)
	case "claim_and_read":
		return h.capStoreClaimAndRead(capName, tableName, params)
	default:
		return errorResponse(fmt.Sprintf("unknown cap_store action %q (allowed: create_table, upsert, query, delete, list_tables, claim_and_read)", action))
	}
}

func (h *Handler) capStoreCreateTable(capName, tableName string, params map[string]any) Response {
	if tableName == "" {
		return errorResponse("table name required for create_table")
	}

	columnsRaw, ok := params["columns"]
	if !ok {
		return errorResponse("columns required for create_table")
	}

	columns, err := parseColumnDefs(columnsRaw)
	if err != nil {
		return errorResponse(fmt.Sprintf("invalid columns: %v", err))
	}

	if err := h.store.CapsCreateTable(capName, tableName, columns); err != nil {
		return errorResponse(fmt.Sprintf("create_table: %v", err))
	}

	return jsonResponse(map[string]any{
		"status": "created",
		"table":  fmt.Sprintf("%s.%s", capName, tableName),
	})
}

func (h *Handler) capStoreUpsert(capName, tableName string, params map[string]any) Response {
	if tableName == "" {
		return errorResponse("table name required for upsert")
	}

	data := parseMapParam(params["data"])
	if len(data) == 0 {
		return errorResponse("data object required for upsert")
	}

	id, err := h.store.CapsUpsert(capName, tableName, data)
	if err != nil {
		return errorResponse(fmt.Sprintf("upsert: %v", err))
	}

	return jsonResponse(map[string]any{"id": id, "status": "ok"})
}

func (h *Handler) capStoreQuery(capName, tableName string, params map[string]any) Response {
	if tableName == "" {
		return errorResponse("table name required for query")
	}

	where, _ := params["where"].(string)
	limit := 100
	if l, ok := params["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	offset := 0
	if o, ok := params["offset"].(float64); ok && o > 0 {
		offset = int(o)
	}

	var args []any
	if argsRaw, ok := params["args"].([]any); ok {
		args = argsRaw
	} else if argsStr, ok := params["args"].(string); ok && argsStr != "" {
		if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
			return errorResponse(fmt.Sprintf("args must be a JSON array: %v", err))
		}
	}

	result, err := h.store.CapsQueryPaged(capName, tableName, where, args, limit, offset)
	if err != nil {
		return errorResponse(fmt.Sprintf("query: %v", err))
	}
	if result.Rows == nil {
		result.Rows = []map[string]any{}
	}

	return jsonResponse(map[string]any{
		"rows":        result.Rows,
		"count":       result.Count,
		"total":       result.Total,
		"has_more":    result.HasMore,
		"next_offset": result.NextOffset,
	})
}

func (h *Handler) capStoreDelete(capName, tableName string, params map[string]any) Response {
	if tableName == "" {
		return errorResponse("table name required for delete")
	}

	where, _ := params["where"].(string)
	if where == "" {
		return errorResponse("where clause required for delete")
	}

	var args []any
	if argsRaw, ok := params["args"].([]any); ok {
		args = argsRaw
	} else if argsStr, ok := params["args"].(string); ok && argsStr != "" {
		if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
			return errorResponse(fmt.Sprintf("args must be a JSON array: %v", err))
		}
	}

	affected, err := h.store.CapsDelete(capName, tableName, where, args)
	if err != nil {
		return errorResponse(fmt.Sprintf("delete: %v", err))
	}

	return jsonResponse(map[string]any{"affected": affected, "status": "ok"})
}

func (h *Handler) capStoreListTables(capName string) Response {
	tables, err := h.store.CapsListTables(capName)
	if err != nil {
		return errorResponse(fmt.Sprintf("list_tables: %v", err))
	}
	if tables == nil {
		tables = []storage.TableInfo{}
	}

	return jsonResponse(map[string]any{"tables": tables, "count": len(tables)})
}

// parseColumnDefs converts JSON column definitions to ColumnDef structs.
// Accepts []any (native JSON) or string (JSON-encoded).
func parseColumnDefs(raw any) ([]storage.ColumnDef, error) {
	var arr []any
	switch v := raw.(type) {
	case []any:
		arr = v
	case string:
		if err := json.Unmarshal([]byte(v), &arr); err != nil {
			return nil, fmt.Errorf("columns must be a JSON array: %w", err)
		}
	default:
		return nil, fmt.Errorf("columns must be an array")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("at least one column required")
	}

	var columns []storage.ColumnDef
	for _, item := range arr {
		switch v := item.(type) {
		case map[string]any:
			name, _ := v["name"].(string)
			typ, _ := v["type"].(string)
			if name == "" || typ == "" {
				return nil, fmt.Errorf("each column needs name and type")
			}
			col := storage.ColumnDef{Name: name, Type: typ}
			if u, ok := v["unique"].(bool); ok {
				col.Unique = u
			}
			if pk, ok := v["primary_key"].(bool); ok {
				col.PrimaryKey = pk
			}
			if nn, ok := v["not_null"].(bool); ok {
				col.NotNull = nn
			}
			columns = append(columns, col)
		default:
			b, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("invalid column: %v", item)
			}
			var col storage.ColumnDef
			if err := json.Unmarshal(b, &col); err != nil {
				return nil, fmt.Errorf("invalid column format: %v", err)
			}
			columns = append(columns, col)
		}
	}
	return columns, nil
}

// parseMapParam accepts map[string]any (native) or string (JSON-encoded).
func parseMapParam(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	if s, ok := raw.(string); ok && s != "" {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	return nil
}

func (h *Handler) capStoreClaimAndRead(capName, tableName string, params map[string]any) Response {
	if tableName == "" { return errorResponse("table name required for claim_and_read") }
	where, _ := params["where"].(string)
	if where == "" { return errorResponse("where clause required for claim_and_read") }
	order, _ := params["order"].(string)
	if order == "" { order = "id ASC" }
	var args []any
	if argsRaw, ok := params["args"].([]any); ok { args = argsRaw
	} else if argsStr, ok := params["args"].(string); ok && argsStr != "" {
		if err := json.Unmarshal([]byte(argsStr), &args); err != nil { return errorResponse(fmt.Sprintf("args must be a JSON array: %v", err)) }
	}
	set := parseMapParam(params["set"])
	if len(set) == 0 { return errorResponse("set columns required for claim_and_read") }
	returning := parseStringSlice(params["returning"])
	if len(returning) == 0 { returning = []string{"*"} }
	row, err := h.store.CapsClaimAndRead(capName, tableName, where, order, args, set, returning)
	if err != nil { return errorResponse(fmt.Sprintf("claim_and_read: %v", err)) }
	if row == nil { return jsonResponse(map[string]any{"claimed": false}) }
	return jsonResponse(map[string]any{"claimed": true, "row": row})
}

func parseStringSlice(raw any) []string {
	if arr, ok := raw.([]any); ok {
		out := make([]string, len(arr))
		for i, v := range arr { out[i] = fmt.Sprint(v) }
		return out
	}
	if s, ok := raw.(string); ok && s != "" {
		var arr []string
		if json.Unmarshal([]byte(s), &arr) == nil { return arr }
	}
	return nil
}
