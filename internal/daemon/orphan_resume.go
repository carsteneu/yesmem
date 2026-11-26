package daemon

import (
	"fmt"
	"log"

	"github.com/carsteneu/yesmem/internal/storage"
)

// autoResumeOrphans wires the resume decision to the MCP handler. The decision
// logic and the budget live in storage.AutoResumeOrphans (testable without a
// daemon); this only supplies the "how do I resume" action.
func (h *Handler) autoResumeOrphans(orphans []storage.OrphanedAgent) (attempted, cleared, gaveUp, superseded int) {
	return h.store.AutoResumeOrphans(orphans, func(o storage.OrphanedAgent) error {
		params := map[string]any{"to": o.ID}
		if o.OpenCodeSessionID != "" {
			params["session_id"] = o.OpenCodeSessionID
		}
		resp := h.Handle(Request{Method: "resume_agent", Params: params})
		if resp.Error != "" {
			return fmt.Errorf("%s", resp.Error)
		}
		return nil
	}, log.Printf)
}
