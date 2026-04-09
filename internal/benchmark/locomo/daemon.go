
package locomo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// DaemonClient connects to the running YesMem daemon via Unix socket
// and executes hybrid_search queries (BM25 + vector + RRF fusion).
type DaemonClient struct {
	socketPath string
	dataDir    string
}

// NewDaemonClient creates a client that will connect to the daemon socket.
// dataDir is the parent directory of daemon.sock (e.g. ~/.claude/yesmem).
func NewDaemonClient(dataDir string) *DaemonClient {
	return &DaemonClient{
		socketPath: daemon.SocketPath(dataDir),
		dataDir:    dataDir,
	}
}

// Ping verifies the daemon is reachable and responding.
func (dc *DaemonClient) Ping() error {
	client, err := daemon.DialTimeout(dc.dataDir, 2*time.Second)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", dc.socketPath, err)
	}
	defer client.Close()

	result, err := client.Call("ping", nil)
	if err != nil {
		return fmt.Errorf("daemon ping failed: %w", err)
	}

	var pong string
	if err := json.Unmarshal(result, &pong); err != nil || pong != "pong" {
		return fmt.Errorf("unexpected ping response: %s", string(result))
	}
	return nil
}

// hybridResponse matches the JSON structure returned by handleHybridSearch.
type hybridResponse struct {
	Results []hybridResult `json:"results"`
	Message string         `json:"message"`
}

// hybridResult matches the enriched result maps from enrichRankedResults.
type hybridResult struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Source  string  `json:"source"`
	Project string  `json:"project"`
}

// HybridSearch calls the daemon's hybrid_search method (BM25 + vector + RRF).
// Returns results as []SearchResult for compatibility with the benchmark pipeline.
func (dc *DaemonClient) HybridSearch(query, project string, limit int) ([]SearchResult, error) {
	client, err := daemon.DialTimeout(dc.dataDir, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(12 * time.Second))

	params := map[string]any{
		"query":   query,
		"project": project,
		"limit":   float64(limit), // JSON numbers are float64
	}

	raw, err := client.Call("hybrid_search", params)
	if err != nil {
		return nil, fmt.Errorf("hybrid_search: %w", err)
	}

	var resp hybridResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse hybrid_search response: %w", err)
	}

	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{
			Content: r.Content,
			Score:   r.Score,
		})
	}
	return out, nil
}

// snippetResponse matches the JSON structure returned by search and deep_search handlers.
type snippetResponse struct {
	Results []snippetResult `json:"results"`
}

type snippetResult struct {
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// Search calls the daemon's search method (BM25 keyword search).
func (dc *DaemonClient) Search(query, project string, limit int) ([]SearchResult, error) {
	client, err := daemon.DialTimeout(dc.dataDir, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(12 * time.Second))

	params := map[string]any{
		"query":   query,
		"project": project,
		"limit":   float64(limit),
	}

	raw, err := client.Call("search", params)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	var resp snippetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}

	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{
			Content: r.Snippet,
			Score:   r.Score,
		})
	}
	return out, nil
}

// DeepSearch calls the daemon's deep_search method (includes thinking blocks and command outputs).
func (dc *DaemonClient) DeepSearch(query, project string, limit int) ([]SearchResult, error) {
	client, err := daemon.DialTimeout(dc.dataDir, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(12 * time.Second))

	params := map[string]any{
		"query":             query,
		"project":           project,
		"limit":             float64(limit),
		"include_thinking":  true,
		"include_commands":  true,
	}

	raw, err := client.Call("deep_search", params)
	if err != nil {
		return nil, fmt.Errorf("deep_search: %w", err)
	}

	var resp snippetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse deep_search response: %w", err)
	}

	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{
			Content: r.Snippet,
			Score:   r.Score,
		})
	}
	return out, nil
}

// docsResponse matches the JSON structure returned by docs_search handler.
type docsResponse struct {
	Results []docsResult `json:"results"`
}

type docsResult struct {
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// DocsSearch calls the daemon's docs_search method (indexed documentation search).
func (dc *DaemonClient) DocsSearch(query, project string, limit int) ([]SearchResult, error) {
	client, err := daemon.DialTimeout(dc.dataDir, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(12 * time.Second))

	params := map[string]any{
		"query": query,
		"limit": float64(limit),
	}

	raw, err := client.Call("docs_search", params)
	if err != nil {
		return nil, fmt.Errorf("docs_search: %w", err)
	}

	var resp docsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse docs_search response: %w", err)
	}

	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{
			Content: r.Content,
			Score:   r.Score,
		})
	}
	return out, nil
}
