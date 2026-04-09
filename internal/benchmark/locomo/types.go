
package locomo

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Sample represents one LoCoMo benchmark sample with conversation sessions and QA pairs.
type Sample struct {
	ID           string    `json:"sample_id"`
	SpeakerA     string    `json:"-"`
	SpeakerB     string    `json:"-"`
	Sessions     []Session `json:"-"`
	QA           []QA      `json:"qa"`
	Observations []Observation `json:"-"` // Gold-standard facts per session
}

// Observation is a gold-standard fact from the LoCoMo dataset.
type Observation struct {
	Speaker  string
	Text     string
	Evidence string
}

// Session represents one conversation session extracted from dynamic session_N keys.
type Session struct {
	Index    int
	DateTime string
	Turns    []Turn
}

// Turn represents a single dialogue turn within a session.
type Turn struct {
	Speaker string `json:"speaker"`
	DiaID   string `json:"dia_id"`
	Text    string `json:"text"`
}

// QA represents a question-answer pair with category and evidence references.
type QA struct {
	Question string          `json:"question"`
	Answer   FlexString      `json:"answer"`
	Category int             `json:"category"`
	Evidence []string        `json:"evidence"`
}

// FlexString handles JSON values that can be string or number.
type FlexString string

func (f *FlexString) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	// Try number
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexString(n.String())
		return nil
	}
	*f = FlexString(string(data))
	return nil
}

// ScoredQA returns only QA pairs with categories 1-4 (excludes unanswerable category 5).
func (s *Sample) ScoredQA() []QA {
	var out []QA
	for _, qa := range s.QA {
		if qa.Category >= 1 && qa.Category <= 4 {
			out = append(out, qa)
		}
	}
	return out
}

// CategoryName returns the human-readable name for a LoCoMo QA category.
func CategoryName(cat int) string {
	switch cat {
	case 1:
		return "Single-hop"
	case 2:
		return "Multi-hop"
	case 3:
		return "Temporal"
	case 4:
		return "Open-domain"
	case 5:
		return "Adversarial"
	default:
		return fmt.Sprintf("Unknown(%d)", cat)
	}
}

// rawSample is the intermediate JSON structure for unmarshaling the LoCoMo dataset format.
type rawSample struct {
	SampleID     string          `json:"sample_id"`
	Conversation json.RawMessage `json:"conversation"`
	QA           []QA            `json:"qa"`
	Observation  json.RawMessage `json:"observation"`
}

// ParseDataset reads a LoCoMo JSON dataset from r and returns parsed samples.
// It handles the dynamic session_N / session_N_date_time keys in the conversation object.
func ParseDataset(r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}

	var raws []rawSample
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("unmarshal dataset: %w", err)
	}

	samples := make([]Sample, 0, len(raws))
	for _, raw := range raws {
		s, err := parseRawSample(raw)
		if err != nil {
			return nil, fmt.Errorf("sample %s: %w", raw.SampleID, err)
		}
		samples = append(samples, s)
	}
	return samples, nil
}

func parseRawSample(raw rawSample) (Sample, error) {
	// Parse conversation as a generic map to handle dynamic session_N keys
	var convMap map[string]json.RawMessage
	if err := json.Unmarshal(raw.Conversation, &convMap); err != nil {
		return Sample{}, fmt.Errorf("unmarshal conversation: %w", err)
	}

	s := Sample{
		ID: raw.SampleID,
		QA: raw.QA,
	}

	// Extract speaker names
	if v, ok := convMap["speaker_a"]; ok {
		json.Unmarshal(v, &s.SpeakerA)
	}
	if v, ok := convMap["speaker_b"]; ok {
		json.Unmarshal(v, &s.SpeakerB)
	}

	// Collect session indices by scanning for session_N keys (not _date_time suffix)
	sessionIndices := map[int]bool{}
	for key := range convMap {
		if !strings.HasPrefix(key, "session_") {
			continue
		}
		if strings.HasSuffix(key, "_date_time") {
			continue
		}
		// Extract the number: session_N
		numStr := strings.TrimPrefix(key, "session_")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		sessionIndices[n] = true
	}

	// Parse each session
	for idx := range sessionIndices {
		sess := Session{Index: idx}

		// Parse turns
		turnKey := fmt.Sprintf("session_%d", idx)
		if turnData, ok := convMap[turnKey]; ok {
			if err := json.Unmarshal(turnData, &sess.Turns); err != nil {
				return Sample{}, fmt.Errorf("unmarshal %s: %w", turnKey, err)
			}
		}

		// Parse datetime
		dtKey := fmt.Sprintf("session_%d_date_time", idx)
		if dtData, ok := convMap[dtKey]; ok {
			json.Unmarshal(dtData, &sess.DateTime)
		}

		s.Sessions = append(s.Sessions, sess)
	}

	// Sort sessions by index for deterministic order
	sort.Slice(s.Sessions, func(i, j int) bool {
		return s.Sessions[i].Index < s.Sessions[j].Index
	})

	// Parse observations (gold-standard facts)
	if len(raw.Observation) > 0 {
		var obsMap map[string]json.RawMessage
		if err := json.Unmarshal(raw.Observation, &obsMap); err == nil {
			for _, v := range obsMap {
				// Each value is {"speaker_name": [["text","ref"], ...], "date": "..."}
				// date is a string, speakers are arrays — parse as map[string]json.RawMessage first
				var rawFields map[string]json.RawMessage
				if err := json.Unmarshal(v, &rawFields); err != nil {
					continue
				}
				for speaker, fieldRaw := range rawFields {
					if speaker == "date" {
						continue // skip date field
					}
					var items []json.RawMessage
					if err := json.Unmarshal(fieldRaw, &items); err != nil {
						continue
					}
					for _, item := range items {
						var arr []string
						if err := json.Unmarshal(item, &arr); err != nil || len(arr) < 1 {
							continue
						}
						evidence := ""
						if len(arr) > 1 {
							evidence = arr[1]
						}
						s.Observations = append(s.Observations, Observation{
							Speaker:  speaker,
							Text:     arr[0],
							Evidence: evidence,
						})
					}
				}
			}
		}
	}

	return s, nil
}
