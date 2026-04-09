package proxy

import (
	"encoding/json"
	"regexp"
	"strings"
)

// skillBlock holds a skill's name and full content for re-injection after collapse.
type skillBlock struct {
	Name    string
	Content string
}

// skillBlockPattern matches [skill:name]...[/skill:name] blocks in messages.
var skillBlockPattern = regexp.MustCompile(`\[skill:([^\]]+)\]`)

// injectSkillsAfterArchive inserts active skill blocks as a user message + assistant ack
// after the archive block (Option B). Returns messages unchanged if no skills or no archive.
func injectSkillsAfterArchive(messages []any, skills []skillBlock) []any {
	if len(skills) == 0 {
		return messages
	}

	// Find archive block (user message containing "[Archiv:")
	archiveIdx := -1
	for i, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		text := extractTextFromContent(msg["content"])
		if strings.Contains(text, "[Archiv:") {
			archiveIdx = i
			break
		}
	}

	if archiveIdx < 0 {
		return messages // no archive block — nothing to inject after
	}

	// Build skill content block
	var sb strings.Builder
	for i, sk := range skills {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(buildSkillInjectionBlock(sk.Name, sk.Content))
	}

	skillMsg := map[string]any{
		"role":    "user",
		"content": sb.String(),
	}
	ackMsg := map[string]any{
		"role":    "assistant",
		"content": "Skills loaded.",
	}

	// Insert after archive block: [system, archive, SKILL, ACK, recent...]
	insertIdx := archiveIdx + 1
	result := make([]any, 0, len(messages)+2)
	result = append(result, messages[:insertIdx]...)
	result = append(result, skillMsg, ackMsg)
	result = append(result, messages[insertIdx:]...)
	return result
}

// detectExistingSkillBlocks scans messages for [skill:name] markers and returns
// the set of skill names already present. Used to avoid duplicate injection.
func detectExistingSkillBlocks(messages []any) map[string]bool {
	names := make(map[string]bool)
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		text := ""
		switch c := msg["content"].(type) {
		case string:
			text = c
		case []any:
			for _, block := range c {
				if b, ok := block.(map[string]any); ok {
					if t, ok := b["text"].(string); ok {
						text += t
					}
				}
			}
		}
		for _, match := range skillBlockPattern.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				// Only count opening tags (not closing [/skill:name])
				name := match[1]
				if !strings.HasPrefix(name, "/") {
					names[name] = true
				}
			}
		}
	}
	return names
}

// buildSkillBlocksForThread returns skill blocks for all active skills in a thread.
// Queries the daemon for full content. Used after collapse to re-inject skills.
func (s *Server) buildSkillBlocksForThread(project, threadID string) []skillBlock {
	if s.skillTracker == nil {
		return nil
	}
	active := s.skillTracker.activeSkills(threadID)
	if len(active) == 0 {
		return nil
	}

	var blocks []skillBlock
	for _, name := range active {
		result, err := s.queryDaemon("get_skill_content", map[string]any{
			"name":    name,
			"project": project,
		})
		if err != nil {
			continue
		}
		var resp struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(result, &resp); err != nil || resp.Content == "" {
			continue
		}
		blocks = append(blocks, skillBlock{Name: name, Content: resp.Content})
	}

	// Cap at 3 skills to limit token budget
	if len(blocks) > 3 {
		blocks = blocks[:3]
		if s.logger != nil {
			s.logger.Printf("%s[skill-collapse] capped to 3 skills (had %d active)%s",
				colorOrange, len(active), colorReset)
		}
	}

	return blocks
}
