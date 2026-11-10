package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

// The broadcast channel is not a handover channel.
//
// A DONE is addressed to one orchestrator and is held until the report behind it
// has a verdict. A broadcast has neither: no single reader to hold, no report the
// daemon could check, and every agent in the project as an audience. Leaving it
// open would let the same payload walk around the hold by being published
// instead of sent, so the daemon refuses it by name and points at send_to.

func TestBroadcastRefusesADONEPayload(t *testing.T) {
	h, s := mustHandler(t)
	peer := os.Getpid()
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-done-bc", Project: "/srv/done-bc", Section: "bc",
		SessionID: "opencode:ses_done_bc", Status: "running", PID: peer,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// Both shapes the delivery check treats as a DONE claim, including the one
	// with blank lines in front of it.
	for _, content := range []string{
		"[DONE] [DEPLOY: no] [BRANCH: main] [MERGE: no] the section is complete",
		"\n\nDONE: Section 'x' is complete.",
	} {
		resp := h.Handle(Request{Method: "broadcast", Params: map[string]any{
			"project": "/srv/done-bc", "content": content, "_peer_pid": peer,
		}})
		if resp.Error == "" {
			t.Fatalf("a DONE payload was broadcast: %q", content)
		}
		if !strings.Contains(resp.Error, "send_to") {
			t.Errorf("the refusal does not name the channel a DONE belongs to: %q", resp.Error)
		}
	}

	msgs, err := s.GetUnreadBroadcasts("reader", "/srv/done-bc")
	if err != nil {
		t.Fatalf("read broadcasts: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("%d DONE row(s) were stored on the broadcast channel: %+v", len(msgs), msgs)
	}

	// Every other announcement still works: the refusal is about the DONE, not
	// about the channel.
	resp := h.Handle(Request{Method: "broadcast", Params: map[string]any{
		"project": "/srv/done-bc", "content": "PROXY HEALTH [OK]", "_peer_pid": peer,
	}})
	if resp.Error != "" {
		t.Fatalf("an ordinary broadcast was refused: %s", resp.Error)
	}
	if msgs, err := s.GetUnreadBroadcasts("reader", "/srv/done-bc"); err != nil || len(msgs) != 1 {
		t.Fatalf("the ordinary broadcast was not stored: %d rows, err=%v", len(msgs), err)
	}
}
