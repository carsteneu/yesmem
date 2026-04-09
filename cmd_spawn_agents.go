package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/orchestrator"
)

func runSpawnAgents(args []string) {
	fs := flag.NewFlagSet("spawn-agents", flag.ExitOnError)
	project := fs.String("project", "", "Project name (required)")
	count := fs.Int("count", 1, "Number of agents to spawn (1-10)")
	tasks := fs.String("tasks", "", "Comma-separated section names for agents")
	callerSession := fs.String("caller-session", "", "Session ID of the calling agent (for send_to callback)")
	fs.Parse(args)

	if *project == "" {
		fmt.Fprintln(os.Stderr, "Error: --project is required")
		os.Exit(1)
	}
	if *count < 1 || *count > 10 {
		fmt.Fprintln(os.Stderr, "Error: --count must be between 1 and 10")
		os.Exit(1)
	}

	sections := buildSections(*tasks, *count)
	terminal := orchestrator.DetectTerminal()
	fmt.Printf("Terminal detected: %s\n", terminal)

	dataDir := yesmemDataDir()
	yesmemBin, _ := os.Executable()

	// Connect to daemon for agent state management
	client, err := daemon.DialTimeout(dataDir, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	type spawnedAgent struct {
		id      string
		section string
		bridge  *orchestrator.AgentBridge
	}

	spawned := make([]spawnedAgent, 0, *count)

	for i := 0; i < *count; i++ {
		section := sections[i]

		// 1. Create agent record in DB via daemon RPC
		client.SetDeadline(time.Now().Add(10 * time.Second))
		result, err := client.Call("spawn_agent", map[string]any{
			"project":        *project,
			"section":        section,
			"caller_session": *callerSession,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-%d: spawn_agent RPC failed: %v\n", i, err)
			continue
		}

		var spawnResult struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(result, &spawnResult); err != nil {
			fmt.Fprintf(os.Stderr, "agent-%d: parse spawn result: %v\n", i, err)
			continue
		}

		// 2. Create PTY bridge locally
		prompt := buildAgentPrompt(*project, section, *callerSession)
		sockPath := filepath.Join(dataDir, fmt.Sprintf("%s.sock", spawnResult.ID))
		bridge, err := orchestrator.NewAgentBridge("claude", []string{"--session-id", spawnResult.SessionID, "--name", fmt.Sprintf("%s-%s", *project, section), prompt}, sockPath, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: bridge failed: %v\n", spawnResult.ID, err)
			continue
		}

		go bridge.Serve()

		// 3. Open terminal window
		bin, spawnArgs := orchestrator.BuildSpawnCommand(terminal, yesmemBin, "", "agent-tty", "--sock", sockPath)
		termCmd := exec.Command(bin, spawnArgs...)
		if err := termCmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: terminal failed: %v\n", spawnResult.ID, err)
			bridge.Close()
			continue
		}

		// 4. Register PID + socket path with daemon
		client.SetDeadline(time.Now().Add(5 * time.Second))
		client.Call("register_agent", map[string]any{
			"id":        spawnResult.ID,
			"pid":       float64(bridge.Cmd.Process.Pid),
			"sock_path": sockPath,
		})

		fmt.Printf("Spawned %s -> section '%s' (PID %d, session %s)\n", spawnResult.ID, section, bridge.Cmd.Process.Pid, spawnResult.SessionID)

		spawned = append(spawned, spawnedAgent{
			id:      spawnResult.ID,
			section: section,
			bridge:  bridge,
		})
	}

	if len(spawned) == 0 {
		fmt.Fprintln(os.Stderr, "No agents could be spawned.")
		os.Exit(1)
	}

	// Wait for all claude processes to finish
	var wg sync.WaitGroup
	for _, a := range spawned {
		wg.Add(1)
		go func(ag spawnedAgent) {
			defer wg.Done()
			defer ag.bridge.Close()
			exitErr := ag.bridge.Cmd.Wait()

			// Update daemon with final status
			status := "finished"
			errMsg := ""
			if exitErr != nil {
				status = "error"
				errMsg = exitErr.Error()
				fmt.Fprintf(os.Stderr, "%s finished with error: %v\n", ag.id, exitErr)
			} else {
				fmt.Printf("%s finished\n", ag.id)
			}

			c, err := daemon.DialTimeout(dataDir, 3*time.Second)
			if err == nil {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				fields := map[string]any{"status": status, "stopped_at": time.Now().UTC().Format(time.RFC3339)}
				if errMsg != "" {
					fields["error"] = errMsg
				}
				c.Call("update_agent", map[string]any{"id": ag.id, "fields": fields})
			}
		}(a)
	}
	wg.Wait()
	fmt.Printf("All %d agents finished.\n", len(spawned))
}

// buildSections returns a slice of length count. Entries from the comma-separated
// tasks string are used first; any remaining slots get a default "agent-N" name.
func buildSections(tasks string, count int) []string {
	sections := make([]string, count)
	if tasks != "" {
		parts := strings.Split(tasks, ",")
		for i, p := range parts {
			if i >= count {
				break
			}
			sections[i] = strings.TrimSpace(p)
		}
	}
	for i := range sections {
		if sections[i] == "" {
			sections[i] = fmt.Sprintf("agent-%d", i)
		}
	}
	return sections
}

// buildAgentPrompt returns the initial instruction for each spawned agent.
func buildAgentPrompt(project, section, callerSession string) string {
	prompt := fmt.Sprintf(
		"You are working on project '%s', section '%s'. "+
			"FIRST ACTION: Immediately write scratchpad_write(project=\"%s\", section=\"%s\", content=\"Status: started\") so the main agent can see you are working. "+
			"Then read scratchpad_read(project=\"%s\") for context and work through the task. "+
			"Write your results with scratchpad_write(project=\"%s\", section=\"%s\", content=...).",
		project, section, project, section, project, project, section,
	)
	if callerSession != "" {
		prompt += fmt.Sprintf(
			" When you are DONE, send send_to(target=\"%s\", content=\"DONE: Section '%s' in project '%s' is finished.\") so the main agent is notified.",
			callerSession, section, project,
		)
	}
	return prompt
}
