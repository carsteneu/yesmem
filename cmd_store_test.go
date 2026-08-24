package main

import (
	"encoding/json"
	"testing"
)

func TestParseStoreArgs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCap string
		wantAct string
		wantTbl string
		wantErr bool
	}{
		{
			name:    "query with where and args",
			input:   `{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}`,
			wantCap: "telegram",
			wantAct: "query",
			wantTbl: "config",
		},
		{
			name:    "upsert with data object",
			input:   `{"capability":"telegram","action":"upsert","table":"messages","data":{"telegram_id":123,"text":"hello"}}`,
			wantCap: "telegram",
			wantAct: "upsert",
			wantTbl: "messages",
		},
		{
			name:    "args as string passthrough",
			input:   `{"capability":"telegram","action":"query","table":"config","where":"key=?","args":"[\"bot_token\"]"}`,
			wantCap: "telegram",
			wantAct: "query",
			wantTbl: "config",
		},
		{
			name:    "missing capability",
			input:   `{"action":"query","table":"config"}`,
			wantErr: true,
		},
		{
			name:    "missing action",
			input:   `{"capability":"telegram","table":"config"}`,
			wantErr: true,
		},
		{
			name:    "missing table for non-list_tables",
			input:   `{"capability":"telegram","action":"query"}`,
			wantErr: true,
		},
		{
			name:    "list_tables needs no table",
			input:   `{"capability":"telegram","action":"list_tables"}`,
			wantCap: "telegram",
			wantAct: "list_tables",
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := parseStoreArgs(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if params["capability"] != tt.wantCap {
				t.Errorf("capability = %v, want %v", params["capability"], tt.wantCap)
			}
			if params["action"] != tt.wantAct {
				t.Errorf("action = %v, want %v", params["action"], tt.wantAct)
			}
			if tt.wantTbl != "" && params["table"] != tt.wantTbl {
				t.Errorf("table = %v, want %v", params["table"], tt.wantTbl)
			}
		})
	}
}

func TestParseStoreArgs_ArgsStringification(t *testing.T) {
	input := `{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"]}`
	params, err := parseStoreArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	args, ok := params["args"].(string)
	if !ok {
		t.Fatalf("args should be string, got %T", params["args"])
	}
	var parsed []any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("args string should be valid JSON array: %v", err)
	}
	if len(parsed) != 1 || parsed[0] != "bot_token" {
		t.Errorf("args = %v, want [bot_token]", parsed)
	}
}

func TestParseStoreArgs_ClaimAndReadParams(t *testing.T) {
	input := `{"capability":"rocketchat","action":"claim_and_read","table":"updates","where":"processed=0","order":"id ASC","set":{"processed":1},"returning":["id","text"]}`
	params, err := parseStoreArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	if params["order"] != "id ASC" {
		t.Errorf("order = %v, want 'id ASC'", params["order"])
	}
	set, ok := params["set"].(string)
	if !ok {
		t.Fatalf("set should be string, got %T", params["set"])
	}
	var setObj map[string]any
	if err := json.Unmarshal([]byte(set), &setObj); err != nil {
		t.Fatalf("set string should be valid JSON object: %v", err)
	}
	if setObj["processed"] != float64(1) {
		t.Errorf("set.processed = %v, want 1", setObj["processed"])
	}
	ret, ok := params["returning"].(string)
	if !ok {
		t.Fatalf("returning should be string, got %T", params["returning"])
	}
	var retArr []string
	if err := json.Unmarshal([]byte(ret), &retArr); err != nil {
		t.Fatalf("returning string should be valid JSON array: %v", err)
	}
	if len(retArr) != 2 || retArr[0] != "id" || retArr[1] != "text" {
		t.Errorf("returning = %v, want [id text]", retArr)
	}
}

func TestParseStoreArgs_DataStringification(t *testing.T) {
	input := `{"capability":"telegram","action":"upsert","table":"messages","data":{"telegram_id":123,"text":"hello"}}`
	params, err := parseStoreArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := params["data"].(string)
	if !ok {
		t.Fatalf("data should be string, got %T", params["data"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		t.Fatalf("data string should be valid JSON object: %v", err)
	}
	if parsed["text"] != "hello" {
		t.Errorf("data.text = %v, want hello", parsed["text"])
	}
}
