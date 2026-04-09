package embedding

import (
	"encoding/json"
	"strings"
	"unicode"
)

// WordPieceTokenizer implements BERT-style WordPiece tokenization.
type WordPieceTokenizer struct {
	vocab   map[string]int
	unkID   int
	maxWord int
}

// LoadWordPieceTokenizer loads a HuggingFace tokenizer.json file.
func LoadWordPieceTokenizer(data []byte) (*WordPieceTokenizer, error) {
	var tj struct {
		Model struct {
			Vocab map[string]int `json:"vocab"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, err
	}
	unkID := tj.Model.Vocab["[UNK]"]
	return &WordPieceTokenizer{vocab: tj.Model.Vocab, unkID: unkID, maxWord: 200}, nil
}

// Tokenize converts text to token IDs using BERT WordPiece.
func (t *WordPieceTokenizer) Tokenize(text string) []int {
	text = strings.ToLower(text)
	words := basicTokenize(text)

	var tokens []int
	for _, word := range words {
		tokens = append(tokens, t.wordPiece(word)...)
	}
	return tokens
}

func basicTokenize(text string) []string {
	var words []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		} else if unicode.IsPunct(r) || isCJK(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			words = append(words, string(r))
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

func (t *WordPieceTokenizer) wordPiece(word string) []int {
	if len(word) > t.maxWord {
		return []int{t.unkID}
	}

	var tokens []int
	start := 0
	for start < len(word) {
		end := len(word)
		found := false
		for end > start {
			substr := word[start:end]
			if start > 0 {
				substr = "##" + substr
			}
			if id, ok := t.vocab[substr]; ok {
				tokens = append(tokens, id)
				found = true
				break
			}
			end--
		}
		if !found {
			return []int{t.unkID}
		}
		start = end
	}
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF)
}
