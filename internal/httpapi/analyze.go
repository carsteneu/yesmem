package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/carsteneu/yesmem/internal/proxy"
)

// AnalyzeTurnRequest is the input for POST /api/analyze-turn.
type AnalyzeTurnRequest struct {
	SessionID   string  `json:"session_id"`
	Project     string  `json:"project"`
	InjectedIDs []int64 `json:"injected_ids"`
	Messages    []any   `json:"messages"`
}

// AnalyzeTurnResponse is the output of POST /api/analyze-turn.
type AnalyzeTurnResponse struct {
	UsedIDs        []int64  `json:"used_ids"`
	NoiseIDs       []int64  `json:"noise_ids"`
	GapTopics      []string `json:"gap_topics"`
	Contradictions []string `json:"contradictions"`
}

// handleAnalyzeTurn scans the last assistant turn for inline reflection signals,
// cross-checks them against injected IDs, and fires fire-and-forget RPC calls
// to bump use/noise/gap/contradiction counts in the daemon.
func (s *Server) handleAnalyzeTurn(w http.ResponseWriter, r *http.Request) {
	var req AnalyzeTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// Extract inline signals from assistant messages.
	signals := proxy.ScanAssistantSignals(req.Messages)

	// Cross-check signals against what was actually injected.
	usedIDs, noiseIDs := crossCheckIDs(signals.UsedIDs, req.InjectedIDs)

	// Fire-and-forget RPC calls to update counts in daemon.
	if s.handler != nil {
		if len(usedIDs) > 0 {
			ids := make([]any, len(usedIDs))
			for i, id := range usedIDs {
				ids[i] = id
			}
			go s.handler.Handle(RPCRequest{
				Method: "increment_use",
				Params: map[string]any{"ids": ids},
			})
		}

		if len(noiseIDs) > 0 {
			ids := make([]any, len(noiseIDs))
			for i, id := range noiseIDs {
				ids[i] = id
			}
			go s.handler.Handle(RPCRequest{
				Method: "increment_noise",
				Params: map[string]any{"ids": ids},
			})
		}

		for _, topic := range signals.GapTopics {
			topic := topic // capture
			go s.handler.Handle(RPCRequest{
				Method: "track_gap",
				Params: map[string]any{
					"topic":      topic,
					"session_id": req.SessionID,
					"project":    req.Project,
				},
			})
		}

		for _, contradiction := range signals.Contradictions {
			contradiction := contradiction // capture
			go s.handler.Handle(RPCRequest{
				Method: "flag_contradiction",
				Params: map[string]any{
					"description": contradiction,
					"session_id":  req.SessionID,
					"project":     req.Project,
				},
			})
		}
	}

	resp := AnalyzeTurnResponse{
		UsedIDs:        usedIDs,
		NoiseIDs:       noiseIDs,
		GapTopics:      signals.GapTopics,
		Contradictions: signals.Contradictions,
	}
	if resp.UsedIDs == nil {
		resp.UsedIDs = []int64{}
	}
	if resp.NoiseIDs == nil {
		resp.NoiseIDs = []int64{}
	}
	if resp.GapTopics == nil {
		resp.GapTopics = []string{}
	}
	if resp.Contradictions == nil {
		resp.Contradictions = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// crossCheckIDs computes:
//   - usedIDs  = intersection(signalIDs, injectedIDs) — in injectedIDs order
//   - noiseIDs = injectedIDs - usedIDs               — injected but not referenced
func crossCheckIDs(signalIDs, injectedIDs []int64) (usedIDs, noiseIDs []int64) {
	if len(injectedIDs) == 0 {
		return []int64{}, []int64{}
	}

	signalSet := make(map[int64]bool, len(signalIDs))
	for _, id := range signalIDs {
		signalSet[id] = true
	}

	usedIDs = []int64{}
	noiseIDs = []int64{}
	for _, id := range injectedIDs {
		if signalSet[id] {
			usedIDs = append(usedIDs, id)
		} else {
			noiseIDs = append(noiseIDs, id)
		}
	}
	return usedIDs, noiseIDs
}
