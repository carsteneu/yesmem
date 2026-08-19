package proxy

import (
	"bytes"
	"strings"
)

// echoScrubber strips a leading run of injected metadata markers from a stream
// of text-delta chunks. Qwen/ChatML models mirror our injected [<ts>] [msg:N]
// [+delta] annotations and meta-inject lines (think-reminder/skill-eval/rules/
// ts-hint) at the very start of their reply; those markers must never reach the
// client. Once real content has begun, everything passes through untouched.
//
// The scrubber never guesses: it only drops bytes that are a complete, fully
// recognized marker line or block, or holds chunks that could still grow into
// one. The moment the leading text can no longer be a marker, it flushes as
// content and permanently switches to pass-through (done). False-positive
// (eating real content) is the failure mode we avoid; a leftover marker is
// merely cosmetic.
type echoScrubber struct {
	pending  []byte // leading bytes not yet classified as marker or content
	done     bool   // real content has started — pass everything through
	cap      int    // max pending bytes before forcing a content decision
	inBlock  bool   // inside a mirrored <system-reminder>...</system-reminder> block
	blockTag []byte // closing tag of the current block, if inBlock
}

const (
	sysReminderOpen  = "<system-reminder>"
	sysReminderClose = "</system-reminder>"
)

func newEchoScrubber() *echoScrubber {
	return &echoScrubber{cap: 4096}
}

// Write accepts one text-delta chunk and returns the bytes that may be written
// to the client. A nil return means "emit nothing for this chunk" — the leading
// bytes are (part of) a marker run that has not yet reached real content.
func (e *echoScrubber) Write(p []byte) []byte {
	if e.done || len(p) == 0 {
		return p
	}
	e.pending = append(e.pending, p...)
	return e.consume()
}

func (e *echoScrubber) flushContent() []byte {
	out := e.pending
	e.pending = nil
	e.done = true
	return out
}

func (e *echoScrubber) consume() []byte {
	for {
		if len(e.pending) == 0 {
			return nil
		}
		// Safety valve: never buffer unboundedly. Flush everything as content.
		if len(e.pending) > e.cap {
			return e.flushContent()
		}

		// Inside a mirrored <system-reminder>…</system-reminder> block: drop
		// everything up to and including the closing tag plus its trailing
		// newline (the block was injected as "[think-reminder] …</system-reminder>\n").
		if e.inBlock {
			closeIdx := bytes.Index(e.pending, e.blockTag)
			if closeIdx < 0 {
				// Still inside, possibly split across chunks. Hold until the
				// closing tag arrives — this is a known mirrored construct.
				return nil
			}
			after := closeIdx + len(e.blockTag)
			if after < len(e.pending) && e.pending[after] == '\n' {
				after++
			}
			e.pending = e.pending[after:]
			e.inBlock = false
			e.blockTag = nil
			continue
		}

		nl := bytes.IndexByte(e.pending, '\n')
		if nl < 0 {
			// Single partial line. Hold only while it could still grow into a
			// fully formed marker; otherwise flush as content.
			ls := string(e.pending)
			if knownInjectPrefixCandidate(ls) {
				return nil
			}
			if hasMetaPrefix(ls) {
				rest := stripMetaPrefixText(ls)
				if rest == "" || rest == ls {
					return nil // pure marker so far, or still incomplete
				}
				// Fully formed marker glued to content on the same line:
				// drop the marker bytes, flush the remainder as content.
				e.pending = e.pending[len(ls)-len(rest):]
				return e.flushContent()
			}
			if strings.HasPrefix(ls, sysReminderOpen) {
				// Block opener split across chunks: hold until this line completes.
				return nil
			}
			if metaPrefixCandidate(ls) {
				return nil
			}
			return e.flushContent()
		}

		// A complete line is available (up to and including '\n').
		line := string(e.pending[:nl])
		if strings.HasPrefix(line, sysReminderOpen) {
			// Mirrored <system-reminder> block opener (raw text injection from
			// buildThinkReminder). Discard through the closing tag.
			e.pending = e.pending[nl+1:]
			e.inBlock = true
			e.blockTag = []byte(sysReminderClose)
			continue
		}
		if isKnownInjectLine(line) {
			// Whole inject line (tag + its text) is a marker.
			e.pending = e.pending[nl+1:]
			continue
		}
		if hasMetaPrefix(line) {
			rest := stripMetaPrefixText(line)
			if rest == "" {
				// Pure annotation line ([<ts>] [msg:N] [+delta]).
				e.pending = e.pending[nl+1:]
				continue
			}
			if rest != line {
				// Marker glued to content on the same line.
				e.pending = e.pending[len(line)-len(rest):]
				return e.flushContent()
			}
		}
		// Real content line — flush everything, then pass through forever.
		return e.flushContent()
	}
}

// knownInjectPrefixCandidate reports whether s (no trailing newline) could still
// form a known inject line: it starts a known tag, or the tag is already fully
// present (inject text may follow until the newline).
func knownInjectPrefixCandidate(s string) bool {
	for _, p := range knownInjectLinePrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
		if len(s) < len(p) && p[:len(s)] == s {
			return true
		}
	}
	return false
}

// metaPrefixCandidate reports whether s, which starts with '[', could still
// become a full annotation line (one where "[msg:" must appear within 40 chars).
func metaPrefixCandidate(s string) bool {
	if len(s) == 0 || s[0] != '[' {
		return false
	}
	if strings.Contains(s, "[msg:") {
		return true
	}
	return len(s) < 42 // still room to form "[...] [msg:"
}
