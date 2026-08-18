package anthropic

import (
	"encoding/json"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

// Wire types for the Anthropic Messages API.

type messagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []wireMessage   `json:"messages"`
	System    json.RawMessage `json:"system,omitempty"` // string or []textBlock

	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice `json:"tool_choice,omitempty"`
	Thinking      *wireThinking   `json:"thinking,omitempty"`
	Metadata      *wireMetadata   `json:"metadata,omitempty"`
}

type wireMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

type wireThinking struct {
	Type         string `json:"type"` // enabled | disabled
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type wireTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	Type         string          `json:"type,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

// cacheControl is Anthropic's prompt-cache breakpoint. Only "ephemeral"
// exists; TTL is optional and the account default applies without it.
type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func (c *cacheControl) hint() *canonical.CacheHint {
	if c == nil {
		return nil
	}
	return &canonical.CacheHint{TTL: c.TTL}
}

func cacheControlFrom(h *canonical.CacheHint) *cacheControl {
	if h == nil {
		return nil
	}
	return &cacheControl{Type: "ephemeral", TTL: h.TTL}
}

type wireToolChoice struct {
	Type                   string `json:"type"` // auto | any | tool | none
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []block
}

// block covers every content block type in both directions. Anthropic uses a
// single discriminated union for request and response content.
type block struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking / redacted_thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// ExtraContent carries a Gemini thought signature across a protocol that
	// has no field for one. See internal/protocol/extension.go. It is only
	// ever written towards a client, never towards Anthropic itself.
	ExtraContent *protocol.ExtraContent `json:"extra_content,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or []block
	IsError   bool            `json:"is_error,omitempty"`

	// CacheControl marks this block as the end of a cacheable prompt prefix.
	// It is named here rather than left to the extension capture because
	// capture only reaches top-level fields, and this one is nested two deep
	// inside messages[].content[].
	CacheControl *cacheControl `json:"cache_control,omitempty"`

	// image / document
	Source *blockSource `json:"source,omitempty"`
	// Title and Context are document-only hints Anthropic shows the model.
	Title   string `json:"title,omitempty"`
	Context string `json:"context,omitempty"`

	// Raw holds provider-native blocks such as compaction and server-tool
	// results. They are replayed only on an Anthropic-to-Anthropic route.
	Raw json.RawMessage `json:"-"`
}

func (b *block) UnmarshalJSON(data []byte) error {
	type plain block
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = block(p)
	switch b.Type {
	case "", "text", "thinking", "redacted_thinking", "tool_use", "tool_result", "image", "document":
	default:
		b.Raw = append(b.Raw[:0], data...)
	}
	return nil
}

func (b block) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	type plain block
	return json.Marshal(plain(b))
}

// blockSource is Anthropic's attachment union: inline base64, a remote url, or
// a handle for a file uploaded to Anthropic.
type blockSource struct {
	Type      string `json:"type"` // base64 | url | file | text
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

type messagesResponse struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	Model        string     `json:"model"`
	Content      []block    `json:"content"`
	StopReason   string     `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
	Usage        *wireUsage `json:"usage,omitempty"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// --- streaming ------------------------------------------------------------

type streamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *messagesResponse `json:"message,omitempty"`

	// content_block_start / delta / stop
	Index        int    `json:"index"`
	ContentBlock *block `json:"content_block,omitempty"`
	Delta        *struct {
		Type string `json:"type"`

		Text        string `json:"text,omitempty"`
		Thinking    string `json:"thinking,omitempty"`
		Signature   string `json:"signature,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`

		// message_delta
		StopReason   string  `json:"stop_reason,omitempty"`
		StopSequence *string `json:"stop_sequence,omitempty"`
	} `json:"delta,omitempty"`

	Usage *wireUsage `json:"usage,omitempty"`

	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type wireError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
