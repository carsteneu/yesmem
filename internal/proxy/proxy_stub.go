package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// runStubCycle executes the full stub pipeline: strip reminders, compress context,
// stubify, compact, collapse, narrative, re-expand. Used by both sawtooth and legacy paths.
func (s *Server) runStubCycle(messages []any, req map[string]any, reqIdx int, proj string, threadID string, overhead int, totalTokens int, userQuery string, isRetryReq bool) StubResult {
	// Get annotations snapshot
	s.mu.RLock()
	annSnapshot := make(map[string]string, len(s.annotations))
	for k, v := range s.annotations {
		annSnapshot[k] = v
	}
	s.mu.RUnlock()

	// Get pivot moments from daemon (cached, best-effort)
	pivotTexts := s.getPivotMoments()

	// Update narrative before stubbing (skip on retry)
	if !isRetryReq {
		s.narrative.Update(messages, reqIdx)
		if len(pivotTexts) > 0 {
			s.narrative.SetPivotMoments(pivotTexts)
		}
	}

	// Strip redundant system-reminders from older messages (biggest token saver)
	messages = StripReminders(messages, reqIdx)

	// Strip old skill hints from previous turns (before cache calculation)
	messages = stripSkillHints(messages)

	// Calculate stub threshold for messages
	estimateFn := TokenEstimateFunc(func(jsonText string) int {
		return s.countTokens(jsonText)
	})
	model, _ := req["model"].(string)
	stubThreshold := s.effectiveTokenThreshold(model) - overhead
	if stubThreshold < 30000 {
		stubThreshold = 30000
	}

	// Phase 0: Compress old thinking blocks and tool_results
	compressResult := CompressContext(messages, s.cfg.KeepRecent, threadID, estimateFn)
	if compressResult.TokensSaved > 0 {
		s.logger.Printf("[req %d] COMPRESS: %d thinking, %d tool_results compressed, ~%dk tokens saved",
			reqIdx, compressResult.ThinkingCompressed, compressResult.ToolResultsCompressed, compressResult.TokensSaved/1000)
	}

	// === Phase 1: Budget-based cutoff ===
	// Calculate how many tokens we have for message content
	tokenFloor := s.cfg.TokenMinimumThreshold
	if tokenFloor == 0 {
		tokenFloor = stubThreshold * 80 / 100
	}
	// Account for post-pipeline injections (narrative, etc.) that aren't in overhead yet
	narrativeOverhead := s.countTokens(s.narrative.Render())
	contentBudget := tokenFloor - overhead - narrativeOverhead
	if contentBudget < 20000 {
		contentBudget = 20000 // absolute minimum
	}

	// Calculate cutoff: walk from recent→old until budget exhausted
	cutoff := CalcCollapseCutoff(messages, s.cfg.KeepRecent, contentBudget, estimateFn)
	s.logger.Printf("[req %d] BUDGET-SPLIT: len(msgs)=%d, budget=%dk, cutoff=%d",
		reqIdx, len(messages), contentBudget/1000, cutoff)

	// === Phase 2: Collapse everything before cutoff into archive ===
	finalMessages := messages
	var stubResult StubResult

	// Fetch relevant learnings (used by both Collapse and Compress-only path)
	var archiveLearnings []ArchiveLearning
	var archiveFlavors []ArchiveSessionFlavor
	s.sessionStartMu.RLock()
	sessionStart := s.sessionStartTime
	s.sessionStartMu.RUnlock()
	if sessionStart.IsZero() {
		// After proxy restart, sessionStartTime is lost — fall back to 24h ago
		sessionStart = time.Now().Add(-24 * time.Hour)
	}
	{
		// Fetch learnings — resolve project_short via daemon (handles renames/moves)
		resolveResult, _ := s.queryDaemon("resolve_project", map[string]any{"project_dir": proj})
		projShort := proj
		if resolveResult != nil {
			var resolved struct{ ProjectShort string `json:"project_short"` }
			if json.Unmarshal(resolveResult, &resolved) == nil && resolved.ProjectShort != "" {
				projShort = resolved.ProjectShort
			}
		}
		result, err := s.queryDaemon("get_learnings_since", map[string]any{
			"project": projShort,
			"since":   sessionStart.Format(time.RFC3339),
			"limit":   20,
		})
		if err != nil {
			s.logger.Printf("[req %d] learnings fetch failed: %v", reqIdx, err)
		} else if result != nil {
			var items []struct {
				Category  string `json:"category"`
				Content   string `json:"content"`
				CreatedAt string `json:"created_at"`
			}
			if json.Unmarshal(result, &items) == nil {
				for _, item := range items {
					ts, _ := time.Parse(time.RFC3339, item.CreatedAt)
					archiveLearnings = append(archiveLearnings, ArchiveLearning{
						Category:  item.Category,
						Content:   item.Content,
						CreatedAt: ts,
					})
				}
			}
		}

		// Fetch session flavors
		flavorResult, err := s.queryDaemon("get_session_flavors_since", map[string]any{
			"project": projShort,
			"since":   sessionStart.Format(time.RFC3339),
			"limit":   20,
		})
		if err != nil {
			s.logger.Printf("[req %d] flavors fetch failed: %v", reqIdx, err)
		} else if flavorResult != nil {
			var items []struct {
				Flavor    string `json:"flavor"`
				CreatedAt string `json:"created_at"`
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(flavorResult, &items) == nil {
				for _, item := range items {
					archiveFlavors = append(archiveFlavors, ArchiveSessionFlavor{
						Flavor:    item.Flavor,
						CreatedAt: item.CreatedAt,
						SessionID: item.SessionID,
					})
				}
			}
		}
	}

	if cutoff > 0 {
		beforeLen := len(finalMessages)
		finalMessages = CollapseOldMessages(finalMessages, messages, cutoff, sessionStart, time.Now(), archiveLearnings, archiveFlavors, threadID)
		s.logger.Printf("[req %d] COLLAPSE: %d -> %d messages (learnings=%d)", reqIdx, beforeLen, len(finalMessages), len(archiveLearnings))

		// Re-inject active skills after archive block (Option B)
		skillBlocks := s.buildSkillBlocksForThread(proj, threadID)
		if len(skillBlocks) > 0 {
			finalMessages = injectSkillsAfterArchive(finalMessages, skillBlocks)
			s.logger.Printf("%s[req %d] COLLAPSE: re-injected %d skill blocks after archive%s", colorBlue, reqIdx, len(skillBlocks), colorReset)
		}

		// Re-inject rules after collapse (counter reset, immediate injection)
		if rulesBlock := s.rulesInjectAfterCollapse(threadID, proj); rulesBlock != "" {
			finalMessages = injectAssociativeContext(finalMessages, s.formatRulesReminder(rulesBlock, proj), s.cfg.SawtoothEnabled)
			s.logger.Printf("%s[req %d] COLLAPSE: re-injected rules reminder%s", colorBlue, reqIdx, colorReset)
		}

		// Analyze fixation in collapsed messages and persist ratio
		analysis := AnalyzeFixation(messages[:cutoff])
		if analysis.Ratio > 0 {
			s.logger.Printf("%s[req %d] FIXATION: ratio=%.1f%% (%d/%d msgs, %d total)%s",
				colorOrange, reqIdx, analysis.Ratio*100, analysis.FixationMessages, cutoff, analysis.TotalMessages, colorReset)
			go s.persistFixationRatio(threadID, analysis.Ratio)
		}
	} else {
		// Inject learnings even without collapse (compress-only path)
		if len(archiveLearnings) > 0 && compressResult.ToolResultsCompressed > 0 {
			var sb strings.Builder
			sb.WriteString("[Metamemory: relevant learnings from this session]\n")
			for _, l := range archiveLearnings {
				content := l.Content
				if len(content) > 120 {
					content = content[:120] + "..."
				}
				content = strings.ReplaceAll(content, "\n", " ")
				fmt.Fprintf(&sb, "  [%s, %s] %s\n", strings.ToUpper(l.Category), l.CreatedAt.Format("15:04"), content)
			}
			// Append as system-context in the first user message's content
			injectMetamemoryBlock(finalMessages, sb.String())
			s.logger.Printf("[req %d] METAMEMORY: injected %d learnings (compress-only path)", reqIdx, len(archiveLearnings))
		}

		// Fallback: if no collapse needed but still over threshold, use StubifyWithTotal
		// Use actual-based totalTokens for threshold check instead of re-counting
		if totalTokens > stubThreshold {
			s.decay.SetPinnedPaths(s.narrative.ActivePaths())
			stubResult = StubifyWithTotal(messages, stubThreshold, s.cfg.KeepRecent, reqIdx, annSnapshot, pivotTexts, estimateFn, totalTokens, s.decay)
			finalMessages = stubResult.Messages
			s.logger.Printf("[req %d] STUBIFY fallback: %d stubs, %dk→%dk",
				reqIdx, stubResult.StubCount, stubResult.TokensBefore/1000, stubResult.TokensAfter/1000)
		}
	}

	// Inject narrative into system block (replaces previous version)
	narrativeText := s.narrative.Render()
	if narrativeText != "" {
		ReplaceSystemBlock(req, "yesmem-narrative", narrativeText)
	}

	// Strip old narrative messages from stream
	finalMessages = StripOldNarratives(finalMessages)

	// Smart re-expansion: temporarily restore stubs matching user's query
	finalMessages = s.reexpandStubsFor(finalMessages, s.effectiveTokenThreshold(model), userQuery)

	req["messages"] = finalMessages

	// Final TTL upgrade: catch blocks injected after the early-path upgrade (narrative, metamemory)
	if s.cfg.CacheTTL != "" && s.cfg.CacheTTL != "ephemeral" {
		if n := UpgradeCacheTTL(req, s.cfg.CacheTTL); n > 0 {
			s.logger.Printf("[req %d %s] cache TTL final pass: %d blocks → %s", reqIdx, proj, n, s.cfg.CacheTTL)
		}
	}
	if n := EnforceCacheBreakpointLimit(req, maxCacheBreakpoints); n > 0 {
		s.logger.Printf("[req %d %s] prompt cache: trimmed %d surplus breakpoints (final)", reqIdx, proj, n)
	}

	// Recalculate actual tokens after all transformations
	actualMsgTokens := s.countMessageTokens(finalMessages)
	actualTotalTokens := actualMsgTokens + overhead
	finalColor := colorLightGreen
	if compressResult.TokensSaved > 0 || cutoff > 0 {
		finalColor = colorGreen
	}
	s.logger.Printf("%s[req %d %s tid=%s] FINAL: %d msgs, %dk msg-tokens, %dk total, compress=-%dk, stubs=%d, narrative=%db%s",
		finalColor, reqIdx, proj, threadID, len(finalMessages),
		actualMsgTokens/1000, actualTotalTokens/1000,
		compressResult.TokensSaved/1000, stubResult.StubCount, len(narrativeText), colorReset)

	// Track archived topics in narrative (skip on retry)
	if stubResult.StubCount > 0 && !isRetryReq {
		topic := extractArchivedTopic(stubResult.Archived, reqIdx)
		if topic != nil {
			s.narrative.AddArchivedTopic(*topic)
		}
	}

	// Record stats for /health endpoint
	s.stats.RecordRequest(stubResult.StubCount, stubResult.TokensBefore, stubResult.TokensAfter)

	return stubResult
}
