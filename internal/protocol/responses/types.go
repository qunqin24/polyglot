package responses

import (
	"encoding/json"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// Wire types for OpenAI's Responses API.
//
// The shape differs from Chat Completions in ways that matter: the request
// carries `input` items rather than `messages`, the system prompt is
// `instructions`, tool definitions are flat rather than nested under
// "function", and the reply is a list of typed output items.

type responsesRequest struct {
	Model string `json:"model"`
	// Input is a bare string or an array of input items.
	Input json.RawMessage `json:"input"`
	// Instructions is the Responses API's system prompt.
	Instructions string `json:"instructions,omitempty"`

	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	Tools             []wireTool        `json:"tools,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Text              *textConfig       `json:"text,omitempty"`
	Reasoning         *reasoningConfig  `json:"reasoning,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	User              string            `json:"user,omitempty"`

	// Store and PreviousResponseID make a conversation server-side stateful.
	// Canonical preserves them for Responses-to-Responses routing; other
	// encoders report that they cannot express the state.
	Store              *bool  `json:"store,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}

type textConfig struct {
	Format *formatSpec `json:"format,omitempty"`
}

type formatSpec struct {
	Type   string          `json:"type"` // text | json_object | json_schema
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

type reasoningConfig struct {
	Effort string `json:"effort,omitempty"`
	// Summary asks the model to expose a summary of its reasoning.
	Summary string `json:"summary,omitempty"`
}

// wireTool is flat: unlike Chat Completions there is no nested "function".
type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

// item is the union used for both request input and response output.
type item struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`

	// message
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"` // string or []contentPart
	Status  string          `json:"status,omitempty"`

	// function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// ExtraContent carries a Gemini thought signature across a protocol that
	// has no field for one. See internal/protocol/extension.go.
	ExtraContent *protocol.ExtraContent `json:"extra_content,omitempty"`

	// function_call_output
	Output string `json:"output,omitempty"`

	// reasoning
	Summary          []summaryPart `json:"summary,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`

	// Raw is set for provider-native output/input items (web_search_call,
	// computer_call, file_search_call, image_generation_call, ...). Those
	// items have no portable canonical shape but must survive a same-protocol
	// route byte-for-byte.
	Raw json.RawMessage `json:"-"`
}

func (i *item) UnmarshalJSON(data []byte) error {
	type plain item
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*i = item(p)
	switch i.Type {
	case "", "message", "function_call", "function_call_output", "reasoning":
	default:
		i.Raw = append(i.Raw[:0], data...)
	}
	return nil
}

func (i item) MarshalJSON() ([]byte, error) {
	if len(i.Raw) > 0 {
		return i.Raw, nil
	}
	type plain item
	return json.Marshal(plain(i))
}

type summaryPart struct {
	Type string `json:"type"` // summary_text
	Text string `json:"text"`
}

type contentPart struct {
	Type string `json:"type"` // input_text | output_text | input_image | input_file | refusal
	Text string `json:"text,omitempty"`

	// input_image: a data: URI, a remote URL, or a handle for a file already
	// uploaded to OpenAI.
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`

	// input_file: inline bytes in a data: URI, or the same kind of handle.
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`

	FileID string `json:"file_id,omitempty"`
}

type responsesResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	CreatedAt int64  `json:"created_at"`
	Model     string `json:"model"`
	// Status is completed | in_progress | incomplete | failed.
	Status            string `json:"status"`
	Output            []item `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage        *wireUsage       `json:"usage,omitempty"`
	Error        *wireErrorDetail `json:"error,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
}

type wireUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

type wireErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

type wireError struct {
	Error wireErrorDetail `json:"error"`
}

// --- streaming ------------------------------------------------------------

// streamEvent covers every event Polyglot reads. Unknown events are ignored,
// which is what keeps the decoder working as OpenAI adds more of them.
type streamEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number,omitempty"`

	Response *responsesResponse `json:"response,omitempty"`

	ItemID       string `json:"item_id,omitempty"`
	OutputIndex  int    `json:"output_index,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
	SummaryIndex int    `json:"summary_index,omitempty"`

	Item  *item        `json:"item,omitempty"`
	Part  *contentPart `json:"part,omitempty"`
	Delta string       `json:"delta,omitempty"`
	Text  string       `json:"text,omitempty"`

	// error events carry the detail inline rather than nested.
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}
