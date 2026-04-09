package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	Token   string
	BaseURL string
	http    *http.Client
}

type Update struct {
	UpdateID int     `json:"update_id"`
	Message  Message `json:"message"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      User   `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID int64 `json:"id"`
}

func NewClient(token string) *Client {
	return &Client{
		Token:   token,
		BaseURL: "https://api.telegram.org/bot" + token,
		http:    &http.Client{Timeout: 35 * time.Second},
	}
}

type apiResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

func (c *Client) GetUpdates(offset int) ([]Update, error) {
	url := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=30", c.BaseURL, offset)
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	if !ar.OK {
		return nil, fmt.Errorf("telegram API error")
	}
	var updates []Update
	return updates, json.Unmarshal(ar.Result, &updates)
}

func (c *Client) SendMessage(chatID int64, text string) error {
	chunks := splitMessage(text, 4096)
	for _, chunk := range chunks {
		url := fmt.Sprintf("%s/sendMessage", c.BaseURL)
		resp, err := c.http.Post(url, "application/json", jsonBody(map[string]any{
			"chat_id":    chatID,
			"text":       chunk,
			"parse_mode": "Markdown",
		}))
		if err != nil {
			return err
		}
		resp.Body.Close()
	}
	return nil
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		end := maxLen
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}

func jsonBody(v any) *bytes.Buffer {
	buf := &bytes.Buffer{}
	json.NewEncoder(buf).Encode(v)
	return buf
}
