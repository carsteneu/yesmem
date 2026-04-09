package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

func runRelay() {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	to := fs.String("to", "", "Target agent ID or section name (required)")
	content := fs.String("content", "", "Message to inject into agent's terminal")
	stop := fs.Bool("stop", false, "Stop the agent gracefully")
	resume := fs.Bool("resume", false, "Resume a stopped agent in a new terminal")
	project := fs.String("project", "", "Project name (for section lookup)")
	fs.Parse(os.Args[2:])

	if *to == "" {
		fmt.Fprintln(os.Stderr, "Error: --to is required")
		fs.Usage()
		os.Exit(1)
	}

	actions := 0
	if *content != "" {
		actions++
	}
	if *stop {
		actions++
	}
	if *resume {
		actions++
	}
	if actions != 1 {
		fmt.Fprintln(os.Stderr, "Error: specify exactly one of --content, --stop, or --resume")
		os.Exit(1)
	}

	dataDir := yesmemDataDir()

	switch {
	case *content != "":
		relayViaDaemon(dataDir, "relay_agent", map[string]any{"to": *to, "content": *content, "project": *project})
	case *stop:
		relayViaDaemon(dataDir, "stop_agent", map[string]any{"to": *to, "project": *project})
	case *resume:
		relayViaDaemon(dataDir, "resume_agent", map[string]any{"to": *to, "project": *project})
	}
}

// relayViaDaemon sends a relay/stop command via daemon RPC.
func relayViaDaemon(dataDir, method string, params map[string]any) {
	client, err := daemon.DialTimeout(dataDir, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	client.SetDeadline(time.Now().Add(10 * time.Second))
	result, err := client.Call(method, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s: %v\n", method, err)
		os.Exit(1)
	}

	printJSON(result)
}
