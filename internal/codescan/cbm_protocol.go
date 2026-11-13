package codescan

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxForeignText bounds text taken from an external process before it reaches
// an error message or a log line. The payload is not our text: without a bound
// a single response floods the daemon log.
const maxForeignText = 200

// quoteForeignText renders text from an external process for an error message:
// whitespace collapsed so it cannot forge a log line, bounded, and quoted so a
// reader can tell foreign text from the message around it.
func quoteForeignText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return `""`
	}
	if len(s) > maxForeignText {
		cut := maxForeignText
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "...(truncated)"
	}
	return strconv.Quote(s)
}

// parseCBMResponse extracts the text payload from the MCP content wrapper
// returned by `cbm cli <tool> ... --json`: {"content":[{"type":"text","text":"..."}]}.
func parseCBMResponse(out []byte) (string, error) {
	var mcpResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(out, &mcpResp); err != nil {
		return "", fmt.Errorf("parse mcp response: %w", err)
	}
	if len(mcpResp.Content) == 0 {
		return "", fmt.Errorf("empty mcp response")
	}
	text := mcpResp.Content[0].Text
	if mcpResp.IsError {
		return "", fmt.Errorf("cbm tool error: %s", quoteForeignText(text))
	}
	return text, nil
}

// parseQueryTable parses v0.10.x query_graph table text:
//
//	rows: 3  (cols: name path)
//	  .PHONY Makefile
//	  build Makefile
//	total: 3
//
// Single-column values may be wrap-quoted ("120708"). Multi-column values are
// space-separated; the scanner's queries select paths, identifiers and counts,
// none of which contain spaces.
func parseQueryTable(text string) ([][]interface{}, error) {
	colCount := 0
	inRefs := false
	refs := make(map[string]string)
	var rows [][]interface{}
	seenHeader := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "rows_refs: "):
			// 0.12.1: repeated values live in a table under this header, and the rows
			// below name them as "@N+suffix".
			seenHeader = true
			inRefs = true
		case strings.HasPrefix(line, "rows_ref_rule:"):
			// The rule the format follows, not data.
		case strings.HasPrefix(line, "rows: "):
			seenHeader = true
			inRefs = false
			rest := strings.TrimPrefix(line, "rows: ")
			if i := strings.Index(rest, "cols: "); i >= 0 {
				spec := strings.TrimSuffix(strings.TrimPrefix(rest[i+len("cols: "):], ")"), ")")
				colCount = len(strings.Fields(spec))
			}
		case strings.HasPrefix(line, "  "):
			if inRefs {
				// "id prefix": the prefix is everything after the first field.
				id, prefix, ok := strings.Cut(strings.TrimSpace(line), " ")
				if !ok {
					return nil, fmt.Errorf("unexpected reference line: %q", line)
				}
				refs[id] = strings.TrimSpace(prefix)
				continue
			}
			if colCount == 0 {
				return nil, fmt.Errorf("unexpected data line: %q", line)
			}
			value := strings.TrimSpace(line)
			row := make([]interface{}, colCount)
			if colCount == 1 {
				row[0] = strings.Trim(value, "\"")
			} else {
				fields := strings.Fields(value)
				for i := 0; i < colCount; i++ {
					if i < len(fields) {
						row[i] = fields[i]
					}
				}
			}
			for i := range row {
				cell, isString := row[i].(string)
				if !isString || !strings.HasPrefix(cell, "@") {
					continue
				}
				resolved, err := resolveCellReference(cell, refs)
				if err != nil {
					return nil, err
				}
				row[i] = resolved
			}
			rows = append(rows, row)
		}
	}
	if !seenHeader {
		return nil, fmt.Errorf("no query table in output: %s", quoteForeignText(text))
	}
	return rows, nil
}

// resolveCellReference expands "@N+suffix" against the 0.12.1 reference table. An id
// outside the table is an error, not an empty prefix: a dropped reference turns a wrong
// path into a plausible one, and the consumer cannot tell.
func resolveCellReference(cell string, refs map[string]string) (string, error) {
	body := strings.TrimPrefix(cell, "@")
	id, suffix, _ := strings.Cut(body, "+")
	prefix, ok := refs[id]
	if !ok {
		return "", fmt.Errorf("reference @%s names no entry of the table", id)
	}
	return prefix + suffix, nil
}
