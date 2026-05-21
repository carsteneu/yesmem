package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

func runLLMComplete(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: yesmem llm-complete '{json}'")
		os.Exit(1)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(args[0]), &raw); err != nil {
		fmt.Fprintln(os.Stderr, "llm-complete: invalid JSON:", err)
		os.Exit(1)
	}

	client, err := daemon.DialTimeout(yesmemDataDir(), 30*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-complete: connect to daemon:", err)
		os.Exit(1)
	}
	defer client.Close()

	resp, err := client.Call("llm_complete", raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-complete: RPC error:", err)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}
