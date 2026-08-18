// Package canonical defines Polyglot's protocol-neutral representation of an
// LLM request and response. Every supported wire protocol decodes into these
// types and encodes back out of them; no protocol ever talks to another
// protocol directly.
package canonical

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type PartType string

const (
	PartText       PartType = "text"
	PartReasoning  PartType = "reasoning"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	// PartNative carries a provider-owned content item that has no portable
	// canonical meaning. It is replayed only to the protocol that issued it.
	PartNative PartType = "native"
	// PartImage and PartFile carry attachments. They are told apart by intent
	// rather than by MIME type alone: every protocol has a distinct wire shape
	// for an image and for a document, and picking the wrong one is a 400.
	PartImage PartType = "image"
	PartFile  PartType = "file"
	// Audio is not converted yet and is still reported as unsupported.
)

// ContentPart is one block inside a message. Exactly one of the payload
// fields is meaningful, selected by Type.
type ContentPart struct {
	Type PartType `json:"type"`

	// Text carries the payload for PartText and PartReasoning.
	Text string `json:"text,omitempty"`

	// Reasoning carries provider-specific reasoning metadata (signatures,
	// redacted blobs) that must survive a round trip to be replayable.
	Reasoning *ReasoningMeta `json:"reasoning,omitempty"`

	ToolCall   *ToolCall      `json:"tool_call,omitempty"`
	ToolResult *ToolResult    `json:"tool_result,omitempty"`
	Native     *NativeContent `json:"native,omitempty"`

	// Media carries the payload for PartImage and PartFile.
	Media *Media `json:"media,omitempty"`

	// Cache marks this part as the end of a cacheable prompt prefix.
	Cache *CacheHint `json:"cache,omitempty"`

	// Signature is a provider-bound replay token attached to this part.
	//
	// Gemini 3 puts a thoughtSignature on the part that closes a thinking
	// block, and for a plain answer that part is the TEXT — not a thought part
	// and not a function call, which have their own homes for it
	// (ReasoningMeta.Signature and ToolCall.Signature). A token with nowhere
	// to live is dropped, the client's history comes back unsigned, and the
	// reasoning behind it can never be replayed.
	//
	// Like every replay token it is meaningful only to the provider that
	// issued it. Never synthesise one, and never send one to an upstream that
	// did not mint it.
	Signature string `json:"signature,omitempty"`
}

// CacheHint marks the end of a prompt prefix an upstream should cache and
// reuse on later requests.
//
// Anthropic is the only protocol that exposes this as a marker a client sets
// (`cache_control`, on a system block, a content block or a tool). OpenAI and
// Gemini cache automatically with no field to set, and Gemini's explicit
// caching is a separate stored-object API rather than a per-block marker.
//
// It lives in canonical rather than in the extension passthrough because
// passthrough only reaches top-level fields. A marker nested inside
// messages[].content[] has no way home, and dropping it silently turns off a
// feature the caller is paying for: the request still succeeds, the reply
// still looks right, and the bill quietly stays at the uncached rate.
type CacheHint struct {
	// TTL is the vendor's own spelling of a lifetime, e.g. "5m" or "1h".
	// Empty means the provider's default.
	TTL string `json:"ttl,omitempty"`
}

// Media is an attachment. Exactly one of Data, URL or FileID identifies the
// bytes; which one a protocol can accept differs, and that difference is the
// whole of multimodal conversion.
//
//	Data   base64, understood by all four protocols — the common case, and
//	       the one that converts with no loss in any direction
//	URL    a remote reference. OpenAI, Anthropic and Responses fetch it
//	       themselves; Gemini does not take arbitrary URLs, so that pairing
//	       is reported unless remote fetching is switched on
//	FileID a handle the provider issued and only it can resolve. Like a
//	       provider-bound replay token, it travels back to the protocol that
//	       minted it and is reported to any other
type Media struct {
	// MIMEType is "image/png", "application/pdf" and so on. Anthropic and
	// Gemini require it; OpenAI folds it into a data URI.
	MIMEType string `json:"mime_type,omitempty"`

	// Data is the base64 payload *without* a data: prefix. It is kept as a
	// string rather than decoded bytes on purpose: every protocol carries
	// base64 on the wire, so decoding here would mean re-encoding on the way
	// out — pure cost on the common path, and double the peak memory for a
	// large attachment.
	Data string `json:"data,omitempty"`

	// URL is a remote http(s) reference supplied by the client.
	URL string `json:"url,omitempty"`

	// FileID references a file already uploaded to a provider. Provider names
	// the protocol that issued it, so an encoder knows whether it can be used.
	FileID   string `json:"file_id,omitempty"`
	Provider string `json:"provider,omitempty"`

	// Filename is shown to the model for documents; some protocols require it.
	Filename string `json:"filename,omitempty"`
	// Detail is OpenAI's resolution hint (auto, low, high). Other protocols
	// have no equivalent and it is reported when it cannot be carried.
	Detail string `json:"detail,omitempty"`
}

// Inline reports whether the bytes travel in the request itself, which every
// protocol can accept.
func (m *Media) Inline() bool { return m != nil && m.Data != "" }

// Remote reports whether this is a URL the target would have to fetch.
func (m *Media) Remote() bool { return m != nil && m.Data == "" && m.URL != "" }

// Bound reports whether this is a provider-issued handle, usable only by the
// protocol named in Provider.
func (m *Media) Bound() bool { return m != nil && m.Data == "" && m.URL == "" && m.FileID != "" }

// Describe names the attachment for a fidelity note without ever including the
// payload — a base64 image in a log line would be both useless and enormous.
func (m *Media) Describe() string {
	if m == nil {
		return "attachment"
	}
	kind := m.MIMEType
	if kind == "" {
		kind = "unknown type"
	}
	switch {
	case m.Filename != "":
		return kind + " " + m.Filename
	case m.Bound():
		return kind + " file " + m.FileID
	case m.Remote():
		return kind + " by URL"
	default:
		return kind
	}
}

// ImagePart builds an inline image part.
func ImagePart(mime, data string) ContentPart {
	return ContentPart{Type: PartImage, Media: &Media{MIMEType: mime, Data: data}}
}

// FilePart builds an inline document part.
func FilePart(mime, filename, data string) ContentPart {
	return ContentPart{Type: PartFile, Media: &Media{MIMEType: mime, Filename: filename, Data: data}}
}

func Text(s string) ContentPart { return ContentPart{Type: PartText, Text: s} }

type ReasoningMeta struct {
	// Provider identifies the wire protocol that issued otherwise-untyped
	// reasoning metadata. It prevents a compatible-only reasoning_content
	// field from being sent to a different protocol by accident.
	Provider string `json:"provider,omitempty"`
	// Signature is Anthropic's thinking-block signature; opaque to us.
	Signature string `json:"signature,omitempty"`
	// Redacted holds an opaque encrypted reasoning payload.
	Redacted string `json:"redacted,omitempty"`
	// ID is the upstream identifier of a reasoning item (OpenAI Responses).
	ID string `json:"id,omitempty"`
}

// NativeContent is an opaque provider content block or output item.
type NativeContent struct {
	Protocol string          `json:"protocol"`
	Type     string          `json:"type"`
	Raw      json.RawMessage `json:"raw"`
}

type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is the raw JSON argument object as produced upstream. It is
	// kept as raw bytes because streamed deltas are only valid JSON once
	// fully accumulated.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Signature is an opaque token the producing provider bound to this call
	// and requires back verbatim when the conversation is replayed. Gemini 3
	// rejects a request whose function calls have lost it. It is meaningful
	// only to the provider that issued it; never synthesise one.
	Signature string `json:"signature,omitempty"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	// Content is the tool output. Normally a single text part.
	Content []ContentPart `json:"content,omitempty"`
	// Structured is the output as JSON, set only when the wire format the
	// result arrived in carried structure rather than a string — Gemini's
	// functionResponse.response and Interactions' result. A protocol that
	// takes an object here sends it unchanged; the text-only ones fall back
	// to Content.
	//
	// Without it a JSON object round trips as a *string containing JSON*,
	// which the model then reads as text rather than as data.
	Structured json.RawMessage `json:"structured,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

type Message struct {
	Role    Role          `json:"role"`
	Name    string        `json:"name,omitempty"`
	Content []ContentPart `json:"content"`
}

// TextContent concatenates all plain text parts of the message.
func (m Message) TextContent() string {
	var s string
	for _, p := range m.Content {
		if p.Type == PartText {
			s += p.Text
		}
	}
	return s
}

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is a JSON Schema object describing the tool input.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Strict     bool            `json:"strict,omitempty"`
	// Cache marks the tool definitions up to and including this one as a
	// cacheable prefix. See CacheHint.
	Cache *CacheHint `json:"cache,omitempty"`
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "tool"
)

type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"`
	// ParallelDisabled mirrors OpenAI's parallel_tool_calls:false /
	// Anthropic's disable_parallel_tool_use:true.
	ParallelDisabled bool `json:"parallel_disabled,omitempty"`
}

type ResponseFormatType string

const (
	FormatText       ResponseFormatType = "text"
	FormatJSONObject ResponseFormatType = "json_object"
	FormatJSONSchema ResponseFormatType = "json_schema"
)

type ResponseFormat struct {
	Type   ResponseFormatType `json:"type"`
	Name   string             `json:"name,omitempty"`
	Schema json.RawMessage    `json:"schema,omitempty"`
	Strict bool               `json:"strict,omitempty"`
}

type ReasoningEffort string

const (
	EffortMinimal ReasoningEffort = "minimal"
	EffortLow     ReasoningEffort = "low"
	EffortMedium  ReasoningEffort = "medium"
	EffortHigh    ReasoningEffort = "high"
)

type ReasoningConfig struct {
	Enabled      bool            `json:"enabled"`
	Effort       ReasoningEffort `json:"effort,omitempty"`
	BudgetTokens *int            `json:"budget_tokens,omitempty"`
	// Visible reports whether the client asked for reasoning text to be
	// included in the response.
	Visible bool `json:"visible,omitempty"`
}

// Request is the protocol-neutral chat request.
type Request struct {
	// Model is the alias as requested by the client. The router replaces it
	// with the upstream model name before encoding.
	Model string `json:"model"`

	// System holds system/developer instructions, kept separate because
	// Anthropic and Gemini model them as a top-level field rather than a
	// message.
	System []ContentPart `json:"system,omitempty"`

	Messages []Message `json:"messages"`

	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`

	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	// PolicyMaxTokens is an internal hard ceiling imposed by the client API
	// key. It is not part of any wire format and protocol encoders may not raise
	// MaxTokens beyond it while satisfying protocol-specific constraints.
	PolicyMaxTokens  *int     `json:"-"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	N                *int     `json:"n,omitempty"`

	Stream         bool             `json:"stream,omitempty"`
	IncludeUsage   bool             `json:"include_usage,omitempty"`
	ResponseFormat *ResponseFormat  `json:"response_format,omitempty"`
	Reasoning      *ReasoningConfig `json:"reasoning,omitempty"`

	User     string            `json:"user,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`

	// Responses API server-side state. It is exact only on a Responses to
	// Responses route; other encoders report it as unsupported.
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	Store              *bool  `json:"store,omitempty"`

	// Extensions holds the fields the decoding codec did not recognise, so a
	// provider's own parameters survive a same-protocol route instead of being
	// dropped without a word. See extensions.go.
	Extensions *Extensions `json:"extensions,omitempty"`

	// NativeTools holds provider-executed tools — Gemini's googleSearch,
	// Anthropic's web_search — which cannot be translated but must survive a
	// same-protocol route.
	NativeTools *NativeTools `json:"native_tools,omitempty"`
}

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishError         FinishReason = "error"
	FinishUnknown       FinishReason = ""
)

// Usage is the token accounting for one request.
//
// InputTokens is the WHOLE prompt — every token the model read, including the
// part served from a cache and the part written to one. The cache fields are
// slices of it, never additions to it.
//
// That has to be stated, because the vendors disagree. OpenAI, OpenAI
// Responses and Gemini all report a prompt total with the cached part called
// out inside it; Anthropic reports the three pieces side by side, with its own
// input_tokens meaning "the part that was neither read from nor written to the
// cache". Left undefined, the same field arrives here meaning two different
// things and every conversion between those two groups is wrong — an OpenAI
// client would be told cached_tokens exceeded prompt_tokens, which in OpenAI's
// accounting cannot happen.
//
// The total is the canonical form because it is the only one of the two from
// which the other can be rebuilt: Anthropic's codec adds the pieces up on the
// way in and splits them apart again on the way out.
type Usage struct {
	// InputTokens is the complete prompt, cache included.
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// CachedInputTokens is the part of InputTokens served from a prompt cache.
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	// CacheWriteTokens is the part of InputTokens written into a cache for
	// later reuse. Only Anthropic bills and reports this separately.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// UncachedInputTokens is the prompt with both cache portions removed: the
// number Anthropic puts in its own input_tokens field. It clamps at zero so a
// hand-built Usage whose parts exceed its total cannot produce a negative
// count on the wire.
func (u Usage) UncachedInputTokens() int {
	n := u.InputTokens - u.CachedInputTokens - u.CacheWriteTokens
	if n < 0 {
		return 0
	}
	return n
}

// Response is a complete, non-streaming model reply.
type Response struct {
	ID           string       `json:"id"`
	Model        string       `json:"model"`
	Created      time.Time    `json:"created"`
	Message      Message      `json:"message"`
	FinishReason FinishReason `json:"finish_reason"`
	Usage        Usage        `json:"usage"`

	// Extensions holds the reply fields the decoding codec did not recognise,
	// re-emitted only to a client speaking the same protocol.
	Extensions *Extensions `json:"extensions,omitempty"`
}

// ToolCalls returns every tool call block in the response message.
func (r *Response) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, p := range r.Message.Content {
		if p.Type == PartToolCall && p.ToolCall != nil {
			out = append(out, *p.ToolCall)
		}
	}
	return out
}

func Ptr[T any](v T) *T { return &v }
