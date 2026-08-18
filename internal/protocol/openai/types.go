package openai

import (
	"encoding/json"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// Wire types for the OpenAI Chat Completions API. They are shared by every
// provider that speaks the OpenAI protocol (OpenRouter, DeepSeek, SiliconFlow,
// Groq, vLLM, Ollama, ...). Vendor differences belong in provider drivers, not
// here.

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`

	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	N                   *int              `json:"n,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *streamOptions    `json:"stream_options,omitempty"`
	Stop                json.RawMessage   `json:"stop,omitempty"` // string or []string
	MaxTokens           *int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	Seed                *int64            `json:"seed,omitempty"`
	ResponseFormat      *responseFormat   `json:"response_format,omitempty"`
	Tools               []wireTool        `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	User                string            `json:"user,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"` // string, array of parts, or null
	Name    string          `json:"name,omitempty"`

	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`

	// ReasoningContent is the de-facto field used by DeepSeek, vLLM and most
	// OpenAI-compatible providers to expose chain-of-thought; Reasoning is
	// OpenRouter's spelling. Both are read, ReasoningContent is written.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`

	Refusal *string `json:"refusal,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    *int   `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent carries a Gemini thought signature across a protocol that
	// has no field for one. See internal/protocol/extension.go.
	ExtraContent *protocol.ExtraContent `json:"extra_content,omitempty"`
}

// contentPart is one element of a structured message content array.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// ImageURL carries either a data: URI or a remote https URL.
	ImageURL *imageURLPart `json:"image_url,omitempty"`
	// File is OpenAI's document attachment: inline base64 with a filename, or
	// a handle for a file already uploaded to OpenAI.
	File *filePart `json:"file,omitempty"`
	// InputAudio is parsed only so it can be reported rather than dropped in
	// silence; audio is not converted yet.
	InputAudio json.RawMessage `json:"input_audio,omitempty"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type filePart struct {
	// FileData is a data: URI carrying the document.
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
	// FileID references a file uploaded to OpenAI; only OpenAI can resolve it.
	FileID string `json:"file_id,omitempty"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage,omitempty"`
}

type wireChoice struct {
	Index        int         `json:"index"`
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// --- streaming ------------------------------------------------------------

type chatChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []wireChunkChoice `json:"choices"`
	Usage   *wireUsage        `json:"usage,omitempty"`
}

type wireChunkChoice struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type wireDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          *string        `json:"content,omitempty"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	Reasoning        *string        `json:"reasoning,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	Refusal          *string        `json:"refusal,omitempty"`
}

type wireError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param,omitempty"`
		Code    any    `json:"code,omitempty"`
	} `json:"error"`
}
