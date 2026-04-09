package proxy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const minCompactableRun = 50

type compactableRun struct {
	start int
	end   int
}

// findCompactableRuns scans the DecayTracker for consecutive sequences of
// Stage-3 stubs that are at least minCompactableRun long.
// scanStart/scanEnd define the message index range to check.
// currentReqIdx is the current request counter (for age calculation).
func findCompactableRuns(dt *DecayTracker, threadLen, scanStart, scanEnd, currentReqIdx int) []compactableRun {
	if dt == nil {
		return nil
	}

	var runs []compactableRun
	runStart := -1

	for i := scanStart; i <= scanEnd; i++ {
		stage := dt.GetStage(i, currentReqIdx, threadLen, 0)
		if stage == DecayStage3 {
			if runStart == -1 {
				runStart = i
			}
		} else {
			if runStart != -1 && (i-1)-runStart+1 >= minCompactableRun {
				runs = append(runs, compactableRun{start: runStart, end: i - 1})
			}
			runStart = -1
		}
	}
	// Close trailing run
	if runStart != -1 && scanEnd-runStart+1 >= minCompactableRun {
		runs = append(runs, compactableRun{start: runStart, end: scanEnd})
	}

	return runs
}

// ArchiveLearning holds a learning for archive block injection.
type ArchiveLearning struct {
	Category  string
	Content   string
	CreatedAt time.Time
}

type compactionStats struct {
	FileStats    string
	ToolStats    string
	Decisions    string
	PivotTexts   string
	Digests      []string               // fallback timeline (when no flavors available)
	SessionStart time.Time              // for temporal annotation
	SessionEnd   time.Time              // for temporal annotation
	Learnings    []ArchiveLearning      // for metamemory (gotchas, unfinished, etc.)
	Flavors      []ArchiveSessionFlavor // session summaries from extraction
	Commits      []GitCommit            // git commits with hash
}

// extractStatsFromMessages extracts file and tool usage stats from a message range.
func extractStatsFromMessages(messages []any, start, end int) compactionStats {
	toolCounts := make(map[string]int)
	fileCounts := make(map[string]int)

	for i := start; i <= end && i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}

			typ, _ := b["type"].(string)
			if typ == "tool_use" {
				name, _ := b["name"].(string)
				if name != "" {
					toolCounts[name]++
				}
				// Extract file paths from input
				if input, ok := b["input"].(map[string]any); ok {
					for _, key := range []string{"file_path", "path", "command"} {
						if v, ok := input[key].(string); ok && v != "" {
							if key == "command" {
								// Skip commands, they're not file paths
								continue
							}
							// Use basename for brevity
							fileCounts[filepath.Base(v)]++
						}
					}
				}
			}
		}
	}

	return compactionStats{
		FileStats: formatCounts(fileCounts),
		ToolStats: formatCounts(toolCounts),
	}
}

// buildCompactedContent creates the replacement text for a compacted block.
func buildCompactedContent(start, end int, stats compactionStats) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Compacted: Messages %d-%d (%d msgs)]", start, end, end-start+1)

	if stats.FileStats != "" {
		fmt.Fprintf(&sb, "\nFiles: %s", stats.FileStats)
	}
	if stats.ToolStats != "" {
		fmt.Fprintf(&sb, "\nTools: %s", stats.ToolStats)
	}
	if stats.Decisions != "" {
		fmt.Fprintf(&sb, "\nDecisions: %s", stats.Decisions)
	}
	if stats.PivotTexts != "" {
		fmt.Fprintf(&sb, "\nPivots: %s", stats.PivotTexts)
	}

	return sb.String()
}

// formatCounts turns a map into "key1(3), key2(1)" sorted by count desc.
func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}

	type kv struct {
		key   string
		count int
	}
	var pairs []kv
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].key < pairs[j].key // deterministic secondary sort
	})

	// Top-5 to keep compacted blocks concise (~500 tokens budget)
	limit := 5
	if len(pairs) < limit {
		limit = len(pairs)
	}

	var parts []string
	for _, p := range pairs[:limit] {
		parts = append(parts, fmt.Sprintf("%s(%d)", p.key, p.count))
	}
	if len(pairs) > 5 {
		parts = append(parts, fmt.Sprintf("+%d more", len(pairs)-5))
	}
	return strings.Join(parts, ", ")
}

// CompactedBlock holds the summary of a compacted run for DB persistence.
type CompactedBlock struct {
	StartIdx int
	EndIdx   int
	Content  string
}

// CompactMessages finds runs of 50+ consecutive Stage 3 stubs,
// builds a summary block from the original messages, and replaces
// the entire run with a single compacted message.
// Returns the compacted messages and blocks for DB storage.
func CompactMessages(modified, original []any, dt *DecayTracker, threadLen, requestIdx int) ([]any, []CompactedBlock) {
	if dt == nil || len(modified) < minCompactableRun {
		return modified, nil
	}

	// Protected zones: skip first message (system), last 4 (keep recent)
	scanStart := 1
	scanEnd := len(modified) - 5
	if scanEnd < scanStart {
		return modified, nil
	}

	runs := findCompactableRuns(dt, threadLen, scanStart, scanEnd, requestIdx)
	if len(runs) == 0 {
		return modified, nil
	}

	var blocks []CompactedBlock

	// Process runs in reverse so indices stay valid
	result := make([]any, len(modified))
	copy(result, modified)

	offset := 0
	for _, run := range runs {
		stats := extractStatsFromMessages(original, run.start, run.end)
		content := buildCompactedContent(run.start, run.end, stats)

		blocks = append(blocks, CompactedBlock{
			StartIdx: run.start,
			EndIdx:   run.end,
			Content:  content,
		})

		// Replace run with single compacted message
		compactedMsg := map[string]any{
			"role":    "user",
			"content": content,
		}

		adjStart := run.start - offset
		adjEnd := run.end - offset
		newResult := make([]any, 0, len(result)-(adjEnd-adjStart))
		newResult = append(newResult, result[:adjStart]...)
		newResult = append(newResult, compactedMsg)
		newResult = append(newResult, result[adjEnd+1:]...)
		result = newResult
		offset += (run.end - run.start) // removed count
	}

	return result, blocks
}
