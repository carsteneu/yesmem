package proxy

import (
	"bytes"
	"regexp"
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

// metaAnnotationRe matches the exact BuildMeta annotation prefix (timestamp
// path "[<wd> YYYY-MM-DD HH:MM:SS] [msg:N] [+delta]" or bare "[msg:N]"). Strict
// by construction: it only fires on the injected annotation shape, so content
// that merely contains a "[msg:..." fragment later in the line is never eaten.
var metaAnnotationRe = regexp.MustCompile(
	`^\[[A-Za-z]+ \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] \[msg:\d+\](?: \[\+[0-9A-Za-z.]+])?|^\[msg:\d+\](?: \[\+[0-9A-Za-z.]+])?`,
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
		// Safety valve: never buffer unboundedly. Strip any verified leading
		// marker run first, then flush the remainder as content.
		if len(e.pending) > e.cap {
			if n := leadingMarkerLen(e.pending); n > 0 {
				e.pending = e.pending[n:]
			}
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
			if m := metaAnnotationRe.FindStringIndex(ls); m != nil {
				if m[1] < len(ls) {
					// Fully formed annotation glued to content on the same line.
					e.pending = e.pending[m[1]:]
					return e.flushContent()
				}
				return nil // pure annotation so far — await newline or more bytes
			}
			if knownInjectPrefixCandidate(ls) {
				return nil
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
		if m := metaAnnotationRe.FindStringIndex(line); m != nil {
			if m[1] == len(line) {
				// Pure annotation line ([<ts>] [msg:N] [+delta]).
				e.pending = e.pending[nl+1:]
				continue
			}
			// Annotation glued to content on the same line.
			e.pending = e.pending[m[1]:]
			return e.flushContent()
		}
		// Real content line — flush everything, then pass through forever.
		return e.flushContent()
	}
}

// leadingMarkerLen returns the byte length of a verified leading marker run at
// the start of buf (annotation lines, known inject lines, and complete
// <system-reminder>…</system-reminder> blocks), or 0 if buf does not begin with
// one. Used so the cap flush still strips confirmed markers before emitting.
func leadingMarkerLen(buf []byte) int {
	n := 0
	for {
		rest := buf[n:]
		if len(rest) == 0 {
			return n
		}
		if nl := bytes.IndexByte(rest, '\n'); nl >= 0 {
			if isKnownInjectLine(string(rest[:nl])) {
				n += nl + 1
				continue
			}
		}
		if m := metaAnnotationRe.FindIndex(rest); m != nil && m[0] == 0 {
			n += m[1]
			if len(rest) > m[1] && rest[m[1]] == '\n' {
				n++
			}
			continue
		}
		if strings.HasPrefix(string(rest), sysReminderOpen) {
			if closeIdx := bytes.Index(rest, []byte(sysReminderClose)); closeIdx >= 0 {
				after := closeIdx + len(sysReminderClose)
				if after < len(rest) && rest[after] == '\n' {
					after++
				}
				n += after
				continue
			}
			break // unclosed block — leave it to the content flush
		}
		break
	}
	return n
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
