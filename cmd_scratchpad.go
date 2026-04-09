package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

func runScratchpad() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: yesmem scratchpad <write|read|list|delete> [flags]")
		fmt.Fprintln(os.Stderr, "  write  --project NAME --section SEC --content TEXT (or - for stdin)")
		fmt.Fprintln(os.Stderr, "  read   --project NAME [--section SEC]")
		fmt.Fprintln(os.Stderr, "  list   [--project NAME]")
		fmt.Fprintln(os.Stderr, "  delete --project NAME [--section SEC]")
		os.Exit(1)
	}

	action := os.Args[2]
	args := os.Args[3:]

	switch action {
	case "write":
		runScratchpadWrite(args)
	case "read":
		runScratchpadRead(args)
	case "list":
		runScratchpadList(args)
	case "delete":
		runScratchpadDelete(args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown scratchpad action: %s\n", action)
		fmt.Fprintln(os.Stderr, "Valid actions: write, read, list, delete")
		os.Exit(1)
	}
}

func runScratchpadWrite(args []string) {
	fs := flag.NewFlagSet("scratchpad write", flag.ExitOnError)
	project := fs.String("project", "", "Project name (required)")
	section := fs.String("section", "", "Section name (required)")
	content := fs.String("content", "", "Content to write (use - to read from stdin)")
	fs.Parse(args)

	if *project == "" {
		fmt.Fprintln(os.Stderr, "Error: --project is required")
		os.Exit(1)
	}
	if *section == "" {
		fmt.Fprintln(os.Stderr, "Error: --section is required")
		os.Exit(1)
	}

	text := *content
	if text == "" || text == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		text = string(raw)
	}

	result, err := scratchpadCall("scratchpad_write", map[string]any{
		"project": *project,
		"section": *section,
		"content": text,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func runScratchpadRead(args []string) {
	fs := flag.NewFlagSet("scratchpad read", flag.ExitOnError)
	project := fs.String("project", "", "Project name (required)")
	section := fs.String("section", "", "Section name (optional, reads all if omitted)")
	fs.Parse(args)

	if *project == "" {
		fmt.Fprintln(os.Stderr, "Error: --project is required")
		os.Exit(1)
	}

	params := map[string]any{"project": *project}
	if *section != "" {
		params["section"] = *section
	}

	result, err := scratchpadCall("scratchpad_read", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func runScratchpadList(args []string) {
	fs := flag.NewFlagSet("scratchpad list", flag.ExitOnError)
	project := fs.String("project", "", "Project name (optional)")
	fs.Parse(args)

	params := map[string]any{}
	if *project != "" {
		params["project"] = *project
	}

	result, err := scratchpadCall("scratchpad_list", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func runScratchpadDelete(args []string) {
	fs := flag.NewFlagSet("scratchpad delete", flag.ExitOnError)
	project := fs.String("project", "", "Project name (required)")
	section := fs.String("section", "", "Section name (optional, deletes all sections if omitted)")
	fs.Parse(args)

	if *project == "" {
		fmt.Fprintln(os.Stderr, "Error: --project is required")
		os.Exit(1)
	}

	params := map[string]any{"project": *project}
	if *section != "" {
		params["section"] = *section
	}

	result, err := scratchpadCall("scratchpad_delete", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

// scratchpadCall dials the daemon and calls a scratchpad RPC method.
func scratchpadCall(method string, params map[string]any) (json.RawMessage, error) {
	client, err := daemon.DialTimeout(yesmemDataDir(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer client.Close()

	client.SetDeadline(time.Now().Add(10 * time.Second))
	return client.Call(method, params)
}

// printJSON writes raw JSON to stdout, pretty-printing if possible.
func printJSON(raw json.RawMessage) {
	if len(raw) == 0 {
		fmt.Println("null")
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not valid JSON — print as-is
		os.Stdout.Write(raw)
		fmt.Println()
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
