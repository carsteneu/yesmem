package proxy

import "strings"

// stripSystemSection removes a markdown section starting with "# header\n"
// from all system blocks. The section ends at the next "\n# " or EOF.
// Returns true if any block was modified.
func stripSystemSection(req map[string]any, header string) bool {
	blocks := ensureSystemArray(req)
	modified := false
	needle := "# " + header + "\n"
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		idx := strings.Index(text, needle)
		if idx < 0 {
			continue
		}
		// Find end of section: next "\n# " after idx+len(needle)
		rest := text[idx+len(needle):]
		end := strings.Index(rest, "\n# ")
		var cleaned string
		if end < 0 {
			// Section goes to EOF — remove from idx
			cleaned = strings.TrimRight(text[:idx], " \t\n")
		} else {
			// Keep everything before idx and from "\n# " onward
			cleaned = text[:idx] + rest[end+1:]
		}
		bm["text"] = cleaned
		blocks[i] = bm
		modified = true
	}
	if modified {
		req["system"] = blocks
	}
	return modified
}

// stripSystemLine removes a specific line from all system blocks.
// Backs up to the previous newline boundary and removes the entire line.
// Returns true if any block was modified.
func stripSystemLine(req map[string]any, line string) bool {
	blocks := ensureSystemArray(req)
	modified := false
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		idx := strings.Index(text, line)
		if idx < 0 {
			continue
		}
		// Find the start of this line (back up to previous newline)
		start := idx
		if start > 0 && text[start-1] == '\n' {
			start--
		}
		// Find the end of this line (include its trailing newline if present)
		end := idx + len(line)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		cleaned := text[:start] + text[end:]
		bm["text"] = cleaned
		blocks[i] = bm
		modified = true
	}
	if modified {
		req["system"] = blocks
	}
	return modified
}

// StripOutputEfficiency removes the "# Output efficiency" markdown section
// from all system prompt blocks. Returns true if any block was modified.
func StripOutputEfficiency(req map[string]any) bool {
	return stripSystemSection(req, "Output efficiency")
}

// StripToneBrevity removes the line "Your responses should be short and concise."
// from all system prompt blocks. Returns true if any block was modified.
func StripToneBrevity(req map[string]any) bool {
	return stripSystemLine(req, "Your responses should be short and concise.")
}

// InjectAntDirectives appends a system block tagged [yesmem-directives] with
// behavioral directives that reinforce honest, verification-first reporting.
func InjectAntDirectives(req map[string]any) {
	const directives = `Before reporting a task complete, verify it actually works: run the test, execute the script, check the output. If you can't verify, say so explicitly rather than claiming success.

Report outcomes faithfully: if tests fail, say so with the relevant output; if you did not run a verification step, say that rather than implying it succeeded. Never claim "all tests pass" when output shows failures, never suppress or simplify failing checks to manufacture a green result, and never characterize incomplete or broken work as done.

If you notice the user's request is based on a misconception, or spot a bug adjacent to what they asked about, say so. You're a collaborator, not just an executor — users benefit from your judgment, not just your compliance.

Err on the side of more explanation. What's most important is the reader understanding your output without mental overhead or follow-ups, not how terse you are.`
	AppendSystemBlock(req, "yesmem-directives", directives)
}

// InjectCLAUDEMDAuthority appends a system block tagged [yesmem-enhance] that
// reinforces the authority of CLAUDE.md and MEMORY.md instructions.
func InjectCLAUDEMDAuthority(req map[string]any) {
	const authority = `The CLAUDE.md and MEMORY.md files contain authoritative project rules and user instructions. Follow them precisely — they represent the user's accumulated decisions and are not optional context.

Comment discipline: Write comments only when the WHY is non-obvious. Do not explain WHAT code does — the code speaks for itself. Do not remove existing comments unless you are removing the code they describe.`
	AppendSystemBlock(req, "yesmem-enhance", authority)
}

// personaTones maps verbosity preference to tone directives.
var personaTones = map[string]string{
	"verbose": "The user prefers detailed explanations. Err on the side of more explanation — show your reasoning, describe trade-offs, and explain why you chose an approach. Thoroughness matters more than brevity.",
	"concise": "The user prefers concise responses. Be direct and efficient, but never sacrifice clarity for brevity. When something needs explanation, explain it fully — then stop.",
}

// InjectPersonaTone appends a system block tagged [yesmem-tone] with a tone
// directive derived from the verbosity preference. No-op if verbosity is empty
// or unknown.
func InjectPersonaTone(req map[string]any, verbosity string) {
	tone, ok := personaTones[verbosity]
	if !ok {
		return
	}
	AppendSystemBlock(req, "yesmem-tone", tone)
}
