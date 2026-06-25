package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/config"
	"gopkg.in/yaml.v3"
)

func runConfig(args []string) {
	dataDir := yesmemDataDir()
	cfgPath := filepath.Join(dataDir, "config.yaml")

	if len(args) == 0 {
		// Show full config
		cfg, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: load: %v\n", err)
			os.Exit(1)
		}
		out, err := yaml.Marshal(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: marshal: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(string(out))
		return
	}

	cmd := args[0]
	switch cmd {
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: yesmem config get <dot-path>")
			fmt.Fprintln(os.Stderr, "  Example: yesmem config get extraction.model")
			os.Exit(1)
		}
		key := args[1]
		value, err := config.GetValue(cfgPath, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config get: %v\n", err)
			os.Exit(1)
		}
		if value == nil {
			fmt.Printf("config.%s: (not set)\n", key)
			return
		}
		// Format nicely
		switch v := value.(type) {
		case string:
			fmt.Println(v)
		case float64, int, bool:
			fmt.Printf("%v\n", v)
		case map[string]any, []any:
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(string(b))
		default:
			fmt.Printf("%v\n", v)
		}

	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: yesmem config set <dot-path> <value>")
			fmt.Fprintln(os.Stderr, "  Example: yesmem config set extraction.model opus")
			os.Exit(1)
		}
		key := args[1]
		value := strings.Join(args[2:], " ")
		if err := config.SetValue(cfgPath, key, config.CoerceValue(value)); err != nil {
			fmt.Fprintf(os.Stderr, "config set: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("config.%s = %s\n", key, value)

	case "show":
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("Config file not found: " + cfgPath)
				fmt.Println("Run 'yesmem setup' to create one.")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "config show: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(string(data))

	default:
		fmt.Fprintf(os.Stderr, "Usage: yesmem config [get|set|show]\n")
		fmt.Fprintln(os.Stderr, "  yesmem config            Show merged config (defaults + overrides)")
		fmt.Fprintln(os.Stderr, "  yesmem config show       Show raw config.yaml")
		fmt.Fprintln(os.Stderr, "  yesmem config get <key>  Get a config value by dot-path")
		fmt.Fprintln(os.Stderr, "  yesmem config set <key> <value>  Set a config value by dot-path")
		os.Exit(1)
	}
}
