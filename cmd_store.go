package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/daemon"
)

func parseStoreArgs(jsonStr string) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	cap, _ := raw["capability"].(string)
	if cap == "" {
		return nil, fmt.Errorf("missing required field: capability")
	}
	action, _ := raw["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("missing required field: action")
	}
	table, _ := raw["table"].(string)
	if action != "list_tables" && table == "" {
		return nil, fmt.Errorf("missing required field: table")
	}

	params := map[string]any{
		"capability": cap,
		"action":     action,
	}
	if table != "" {
		params["table"] = table
	}
	if w, ok := raw["where"].(string); ok {
		params["where"] = w
	}
	if o, ok := raw["order"].(string); ok {
		params["order"] = o
	}
	if l, ok := raw["limit"].(float64); ok {
		params["limit"] = l
	}

	if args, ok := raw["args"]; ok {
		switch v := args.(type) {
		case string:
			params["args"] = v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("cannot serialize args: %w", err)
			}
			params["args"] = string(b)
		}
	}

	if data, ok := raw["data"]; ok {
		switch v := data.(type) {
		case string:
			params["data"] = v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("cannot serialize data: %w", err)
			}
			params["data"] = string(b)
		}
	}

	for _, key := range []string{"set", "returning"} {
		if v, ok := raw[key]; ok {
			if s, isStr := v.(string); isStr {
				params[key] = s
			} else {
				b, err := json.Marshal(v)
				if err != nil {
					return nil, fmt.Errorf("cannot serialize %s: %w", key, err)
				}
				params[key] = string(b)
			}
		}
	}

	return params, nil
}

func runStore(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: yesmem store '<json>'")
		fmt.Fprintln(os.Stderr, `  Example: yesmem store '{"capability":"telegram","action":"query","table":"config","where":"key=?","args":["bot_token"],"limit":1}'`)
		os.Exit(1)
	}

	params, err := parseStoreArgs(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		os.Exit(1)
	}

	client, err := daemon.Dial(filepath.Join(os.Getenv("HOME"), ".claude", "yesmem"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		os.Exit(1)
	}
	result, err := client.Call("cap_store", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		os.Exit(1)
	}

	out, _ := json.Marshal(result)
	fmt.Println(string(out))
}
