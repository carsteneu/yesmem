package daemon

import (
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
)

// planThreadsForAgent lists the threads that can hold this agent's plan, in the
// order the plan store is consulted.
//
// set_plan stores a plan under the thread its caller reports, and a spawned
// agent reports its backend session ("opencode:<ses_…>", "codex:<uuid>"), not
// the daemon's own session uuid in agents.session_id. The proxy derives the same
// thread for the plan checkpoint (anthropicThread, prepareOpenAIRequestContext)
// and storage.AgentCanonicalAddress answers whoami with it, so the backend
// thread is the agent's plan thread with the fewest assumptions.
//
// The other entries are identities the agent carries anyway: a recorded proxy
// thread, the same address without its namespace (storage treats both as one
// identity, the plan table matches exactly), the namespaced daemon uuid the MCP
// layer produces before the backend id is captured, and the bare session uuid
// (the only thread a claude agent has).
//
// The list is only as trustworthy as the agent row: session_id, opencode_… and
// codex_session_id are not writable through update_agent, but proxy_thread_id is
// (YM-344 excludes the first three only). A caller that can write its own row can
// therefore name a thread here; agentPlanThread has read that column all along, so
// this adds no reach, and the fix belongs in the same exclusion set.
func planThreadsForAgent(agent storage.Agent) []string {
	var threads []string
	add := func(thread string) {
		if thread == "" {
			return
		}
		for _, have := range threads {
			if have == thread {
				return
			}
		}
		threads = append(threads, thread)
	}

	add(storage.AgentCanonicalAddress(&agent))
	add(agent.ProxyThreadID)
	if namespace := backendNamespace(agent.Backend); namespace != "" {
		// The same address without its namespace: storage treats "ses_…" and
		// "opencode:ses_…" as one identity, but the plan table matches exactly.
		add(strings.TrimPrefix(storage.AgentCanonicalAddress(&agent), namespace+":"))
		if agent.SessionID != "" && !strings.Contains(agent.SessionID, ":") {
			add(namespace + ":" + agent.SessionID)
		}
	}
	add(agent.SessionID)
	return threads
}

// backendNamespace is the address namespace a backend's sessions carry, mirroring
// storage.agentNamespace: claude has none, its ids are stored bare. A backend
// added there has to be added here too, or its plans are only found under the
// captured address; the duplication is deliberate (storage keeps the namespace
// unexported, and address.go is shared with other work in flight).
func backendNamespace(backend string) string {
	switch backend {
	case "opencode", "codex":
		return backend
	}
	return ""
}
