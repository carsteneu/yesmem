package proxy

// ResponsesRequest represents an OpenAI Responses API request.
// See: https://developers.openai.com/docs/api-reference/responses/create
type ResponsesRequest struct {
	Model              string              `json:"model"`
	Input              any                 `json:"input"`                         // string or []ResponsesInputItem
	Instructions       string              `json:"instructions,omitempty"`        // system-level guidance
	Tools              []ResponsesTool     `json:"tools,omitempty"`               // function definitions
	Store              *bool               `json:"store,omitempty"`               // default true
	PreviousResponseID string              `json:"previous_response_id,omitempty"`
	Stream             bool                `json:"stream,omitempty"`
	MaxOutputTokens    int                 `json:"max_output_tokens,omitempty"`
	Temperature        *float64            `json:"temperature,omitempty"`
	TopP               *float64            `json:"top_p,omitempty"`
	Reasoning          *ResponsesReasoning `json:"reasoning,omitempty"`
}

// ResponsesInputItem is one item in the input array.
type ResponsesInputItem struct {
	Role    string `json:"role"`              // user, assistant, system
	Content any    `json:"content"`           // string or []ContentPart
	Type    string `json:"type,omitempty"`    // "message", "function_call", "function_call_output"
	CallID  string `json:"call_id,omitempty"` // for function_call / function_call_output
	Name    string `json:"name,omitempty"`    // for function_call
	Args    string `json:"arguments,omitempty"` // for function_call (JSON string)
	Output  string `json:"output,omitempty"`  // for function_call_output
	ID      string `json:"id,omitempty"`      // item id
	Status  string `json:"status,omitempty"`  // "completed", "incomplete"
}

// ResponsesTool is a tool definition in the Responses API format.
type ResponsesTool struct {
	Type        string          `json:"type"`                  // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  any             `json:"parameters,omitempty"`  // JSON Schema
	Strict      *bool           `json:"strict,omitempty"`      // default true in Responses API
}

// ResponsesReasoning configures model reasoning.
type ResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`  // "low", "medium", "high"
	Summary string `json:"summary,omitempty"` // "auto", "concise", "detailed"
}

// ResponsesResponse is the full response from the Responses API.
type ResponsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"`     // "response"
	CreatedAt int64                 `json:"created_at"`
	Model     string                `json:"model"`
	Output    []ResponsesOutputItem `json:"output"`
	Usage     *ResponsesUsage       `json:"usage,omitempty"`
	Status    string                `json:"status,omitempty"` // "completed", "incomplete", "failed"
}

// ResponsesOutputItem is one item in the response output array.
type ResponsesOutputItem struct {
	ID      string              `json:"id,omitempty"`
	Type    string              `json:"type"`      // "message", "function_call", "reasoning", "function_call_output"
	Role    string              `json:"role,omitempty"` // "assistant" for message type
	Status  string              `json:"status,omitempty"`
	Content []ResponsesContent  `json:"content,omitempty"` // for message type
	Name    string              `json:"name,omitempty"`    // for function_call
	CallID  string              `json:"call_id,omitempty"` // for function_call
	Args    string              `json:"arguments,omitempty"` // for function_call (JSON string)
	Summary []ResponsesContent  `json:"summary,omitempty"` // for reasoning type
}

// ResponsesContent is a content block in the Responses API.
type ResponsesContent struct {
	Type        string `json:"type"`                    // "output_text", "refusal", "input_text"
	Text        string `json:"text,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
}

// ResponsesUsage tracks token usage in Responses API format.
type ResponsesUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	TotalTokens          int `json:"total_tokens"`
	InputTokensCacheHit  int `json:"input_tokens_details.cached_tokens,omitempty"`
}

// ResponsesStreamEvent is a single SSE event in the Responses API stream.
type ResponsesStreamEvent struct {
	Type     string `json:"type"` // "response.created", "response.output_item.added", "response.content_part.delta", "response.completed", etc.
	Response *ResponsesResponse    `json:"response,omitempty"`
	Item     *ResponsesOutputItem  `json:"item,omitempty"`
	Delta    *ResponsesContentDelta `json:"delta,omitempty"`
}

// ResponsesContentDelta is a streaming delta for content.
type ResponsesContentDelta struct {
	Type string `json:"type,omitempty"` // "text_delta"
	Text string `json:"text,omitempty"`
}
