package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/telegram"
)

func runTelegramBridge() {
	fs := flag.NewFlagSet("telegram-bridge", flag.ExitOnError)
	token := fs.String("token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram Bot API token")
	allowedStr := fs.String("allowed-users", os.Getenv("TELEGRAM_ALLOWED_USERS"), "Comma-separated Telegram user IDs")
	fs.Parse(os.Args[2:])

	if *token == "" {
		fmt.Fprintln(os.Stderr, "Error: --token or TELEGRAM_BOT_TOKEN required")
		os.Exit(1)
	}
	if *allowedStr == "" {
		fmt.Fprintln(os.Stderr, "Error: --allowed-users or TELEGRAM_ALLOWED_USERS required")
		os.Exit(1)
	}

	allowed := parseAllowedUsers(*allowedStr)
	if len(allowed) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no valid user IDs in --allowed-users")
		os.Exit(1)
	}

	client := telegram.NewClient(*token)
	dataDir := yesmemDataDir()
	bridgeSession := fmt.Sprintf("telegram-bridge-%d", time.Now().UnixNano()%100000)

	// Agent state: chatID → agentID
	agents := map[int64]string{}

	log.Printf("Telegram bridge started (session: %s, allowed users: %v)", bridgeSession, allowed)

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("Shutting down — stopping all agents...")
		for chatID, agentID := range agents {
			daemonCall(dataDir, "stop_agent", map[string]any{"to": agentID})
			log.Printf("Stopped agent %s (chat %d)", agentID, chatID)
		}
		os.Exit(0)
	}()

	offset := 0
	for {
		updates, err := client.GetUpdates(offset)
		if err != nil {
			log.Printf("getUpdates error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message.Text == "" {
				continue
			}
			if !allowed[u.Message.From.ID] {
				continue
			}
			handleTelegramMessage(client, dataDir, bridgeSession, agents, u.Message)
		}
	}
}

func handleTelegramMessage(tg *telegram.Client, dataDir, bridgeSession string, agents map[int64]string, msg telegram.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	switch {
	case text == "/status":
		handleStatus(tg, dataDir, chatID, agents)
		return
	case text == "/stop":
		handleStop(tg, dataDir, chatID, agents)
		return
	case text == "/new":
		handleNew(tg, dataDir, chatID, bridgeSession, agents)
		return
	}

	agentID, ok := agents[chatID]
	if !ok {
		var err error
		agentID, err = spawnAgentForChat(dataDir, chatID, bridgeSession)
		if err != nil {
			log.Printf("spawn failed for chat %d: %v", chatID, err)
			tg.SendMessage(chatID, "Could not start agent. Try /new.")
			return
		}
		agents[chatID] = agentID
		log.Printf("Spawned agent %s for chat %d", agentID, chatID)
	}

	_, err := daemonCall(dataDir, "relay_agent", map[string]any{
		"to":             agentID,
		"content":        text,
		"caller_session": bridgeSession,
	})
	if err != nil {
		log.Printf("relay to %s failed: %v", agentID, err)
		tg.SendMessage(chatID, "Agent nicht erreichbar. Versuche /new.")
		delete(agents, chatID)
		return
	}

	response := waitForResponse(dataDir, bridgeSession, 120*time.Second)
	if response == "" {
		tg.SendMessage(chatID, "Agent antwortet nicht (Timeout 120s). Versuche /new.")
		return
	}
	tg.SendMessage(chatID, response)
}

func spawnAgentForChat(dataDir string, chatID int64, bridgeSession string) (string, error) {
	project := fmt.Sprintf("telegram-%d", chatID)
	result, err := daemonCall(dataDir, "spawn_agent", map[string]any{
		"project":        project,
		"section":        "chat",
		"caller_session": bridgeSession,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(result, &resp)
	return resp.ID, nil
}

func waitForResponse(dataDir, bridgeSession string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := daemonCall(dataDir, "get_channel_messages", map[string]any{
			"session_id": bridgeSession,
		})
		if err == nil {
			var msgs []struct {
				Content string `json:"content"`
			}
			json.Unmarshal(result, &msgs)
			if len(msgs) > 0 {
				daemonCall(dataDir, "mark_channel_read", map[string]any{
					"session_id": bridgeSession,
				})
				var parts []string
				for _, m := range msgs {
					parts = append(parts, m.Content)
				}
				return strings.Join(parts, "\n")
			}
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

func handleStatus(tg *telegram.Client, dataDir string, chatID int64, agents map[int64]string) {
	agentID, ok := agents[chatID]
	if !ok {
		tg.SendMessage(chatID, "No active agent. Send a message to start one.")
		return
	}
	result, err := daemonCall(dataDir, "get_agent", map[string]any{"to": agentID})
	if err != nil {
		tg.SendMessage(chatID, fmt.Sprintf("Agent %s nicht erreichbar: %v", agentID, err))
		return
	}
	var agent struct {
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	json.Unmarshal(result, &agent)
	tg.SendMessage(chatID, fmt.Sprintf("Agent: %s\nStatus: %s\nGestartet: %s", agentID, agent.Status, agent.CreatedAt))
}

func handleStop(tg *telegram.Client, dataDir string, chatID int64, agents map[int64]string) {
	agentID, ok := agents[chatID]
	if !ok {
		tg.SendMessage(chatID, "No active agent.")
		return
	}
	daemonCall(dataDir, "stop_agent", map[string]any{"to": agentID})
	delete(agents, chatID)
	tg.SendMessage(chatID, fmt.Sprintf("Agent %s gestoppt.", agentID))
}

func handleNew(tg *telegram.Client, dataDir string, chatID int64, bridgeSession string, agents map[int64]string) {
	if agentID, ok := agents[chatID]; ok {
		daemonCall(dataDir, "stop_agent", map[string]any{"to": agentID})
		delete(agents, chatID)
	}
	agentID, err := spawnAgentForChat(dataDir, chatID, bridgeSession)
	if err != nil {
		tg.SendMessage(chatID, fmt.Sprintf("Error spawning agent: %v", err))
		return
	}
	agents[chatID] = agentID
	tg.SendMessage(chatID, fmt.Sprintf("New agent started: %s", agentID))
}

// daemonCall makes a single RPC call to the daemon.
func daemonCall(dataDir, method string, params map[string]any) (json.RawMessage, error) {
	c, err := daemon.DialTimeout(dataDir, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	return c.Call(method, params)
}

// parseAllowedUsers parses "123,456,789" into a set.
func parseAllowedUsers(s string) map[int64]bool {
	m := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			m[id] = true
		}
	}
	return m
}
