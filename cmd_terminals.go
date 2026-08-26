package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/terminals"
)

func runSaveTerminals() {
	if err := terminals.Save(yesmemDataDir()); err != nil {
		fmt.Fprintf(os.Stderr, "save-terminals: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Terminal-Status gespeichert.")
}

func runRestartTerminals() {
	if err := terminals.Restore(yesmemDataDir()); err != nil {
		fmt.Fprintf(os.Stderr, "restart-terminals: %v\n", err)
		os.Exit(1)
	}
}

// runSpawnTerminal opens a brand-new terminal session (cwd by default) as a
// managed agent, so it can be relayed into like any spawn_agent.
func runSpawnTerminal() {
	workDir := ""
	backend := ""
	for i := 2; i < len(os.Args); i++ {
		switch {
		case os.Args[i] == "--backend" && i+1 < len(os.Args):
			backend = os.Args[i+1]
			i++
		case strings.HasPrefix(os.Args[i], "-"):
			// Flags ohne Wert ignorieren.
		case workDir == "":
			workDir = os.Args[i]
		}
	}
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spawn-terminal: aktuelles Verzeichnis nicht ermittelbar")
			os.Exit(1)
		}
		workDir = cwd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn-terminal: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(abs); err != nil {
		fmt.Fprintf(os.Stderr, "spawn-terminal: Verzeichnis %q nicht gefunden: %v\n", abs, err)
		os.Exit(1)
	}
	id, err := terminals.SpawnNewTerminal(yesmemDataDir(), abs, backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn-terminal: %v (läuft der Daemon?)\n", err)
		os.Exit(1)
	}
	fmt.Printf("Neue Terminal-Session gestartet: Agent %s (in %s)\nReinrelayen: yesmem relay --to %s --content \"...\"\n", id, abs, id)
}
