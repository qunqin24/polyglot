package interactions

import "encoding/json"

// The Interactions wire format.
//
// These structs transcribe the schema the official TypeScript provider
// validates against, cross-checked against recorded traffic. Where Google's
// prose documentation and the recorded traffic disagreed, the recording won —
// see the package comment in codec.go for the two cases.

// --- request --------------------------------------------------------------

type interactionRequest struct {
	Model string `json:"model"`
	// Input is a bare string or an array of steps. Polyglot always sends the
	// array form: it carries the whole conversation, which is what a stateless
	// gateway has to do.
	Input json.RawMessage `json:"input"`

	SystemInstruction string `json:"system_instruction,omitempty"`
	Tools             []tool `json:"tools,omitempty"`
	Stream            bool   `json:"stream,omitempty"`

	GenerationConfig *generationConfig `json:"generation_config,omitempty"`
	ResponseFormat   []responseFormat  `json:"response_format,omitempty"`

	// Store asks Google to keep the conversation server-side, and defaults to
	// true when the field is absent. Polyglot is stateless and always sends
	// false explicitly — see codec.go.
	Store *bool `json:"store,omitempty"`
	// PreviousInteractionID continues a stored conversation. Polyglot cannot
	// use it as a client and reports it when one arrives from a caller.
	PreviousInteractionID string `json:"previous_interaction_id,omitempty"`
	Background            *bool  `json:"background,omitempty"`

	// safety_settings is deliberately not named here. Leaving it unmodelled
	// lets the generic extension capture carry it back to an Interactions
	// upstream unchanged and report it on any other route.
}

type generationConfig struct {
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	MaxTokens     *int     `json:"max_output_tokens,omitempty"`
	ThinkingLevel string   `json:"thinking_level,omitempty"`
	Seed          *int64   `json:"seed,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

type responseFormat struct {
	Type     string          `json:"type"`
	MIMEType string          `json:"mime_type,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
}

// tool is the flat, typed declaration Interactions uses. generateContent
// nests function declarations inside a wrapper object; this does not.
type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// --- steps ----------------------------------------------------------------

// step is the discriminated union that makes up both `steps[]` in a reply and
// the `input[]` array of a request.
//
// Only `user_input` and `model_output` wrap their payload in `content`.
// Function calls, thoughts and the built-in tool steps carry theirs directly
// on the step, which is why this is one wide struct rather than a nesting.
type step struct {
	Type string `json:"type"`

	// user_input / model_output
	Content []contentBlock `json:"content,omitempty"`

	// function_call, and the built-in *_call steps
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// function_result, and the built-in *_result steps
	CallID  string          `json:"call_id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`

	// thought
	Summary []thoughtSummaryItem `json:"summary,omitempty"`

	// Signature is the replay token. It appears on thought, function_call and
	// built-in tool steps, and Google requires it back verbatim on the next
	// turn when the conversation is not stored server-side.
	Signature *string `json:"signature,omitempty"`

	ServerName string `json:"server_name,omitempty"`
	SearchType string `json:"search_type,omitempty"`
	Status     string `json:"status,omitempty"`
}

type thoughtSummaryItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// contentBlock is an element of a user_input or model_output step.
type contentBlock struct {
	Type string `json:"type"`

	// text
	Text        string       `json:"text,omitempty"`
	Annotations []annotation `json:"annotations,omitempty"`

	// image / video
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
	// Resolution is Interactions' spelling of an image detail hint.
	Resolution string `json:"resolution,omitempty"`
}

// annotation is an inline citation. generateContent kept these in a separate
// groundingMetadata object; Interactions attaches them to the text they
// support.
type annotation struct {
	Type       string `json:"type,omitempty"`
	StartIndex *int   `json:"start_index,omitempty"`
	EndIndex   *int   `json:"end_index,omitempty"`
	URI        string `json:"uri,omitempty"`
	Title      string `json:"title,omitempty"`
}

// Step types. The built-in tool steps are enumerated so an unfamiliar one is
// still recognised as provider-executed rather than mistaken for content.
const (
	stepUserInput      = "user_input"
	stepModelOutput    = "model_output"
	stepFunctionCall   = "function_call"
	stepFunctionResult = "function_result"
	stepThought        = "thought"
)

var builtinCallSteps = map[string]bool{
	"google_search_call":   true,
	"code_execution_call":  true,
	"url_context_call":     true,
	"file_search_call":     true,
	"google_maps_call":     true,
	"mcp_server_tool_call": true,
}

var builtinResultSteps = map[string]bool{
	"google_search_result":   true,
	"code_execution_result":  true,
	"url_context_result":     true,
	"file_search_result":     true,
	"google_maps_result":     true,
	"mcp_server_tool_result": true,
}

func isBuiltinStep(t string) bool { return builtinCallSteps[t] || builtinResultSteps[t] }

// --- response -------------------------------------------------------------

type interactionResponse struct {
	ID     string `json:"id,omitempty"`
	Object string `json:"object,omitempty"`
	Model  string `json:"model,omitempty"`
	// Status is completed | in_progress | requires_action | failed.
	// requires_action means the model is waiting on a function result.
	Status string `json:"status,omitempty"`
	Steps  []step `json:"steps,omitempty"`
	Usage  *usage `json:"usage,omitempty"`
	// Created is RFC 3339 and maps to canonical Created, the same field
	// OpenAI spells "created" as a Unix second.
	Created string     `json:"created,omitempty"`
	Error   *wireError `json:"error,omitempty"`

	// "updated" and "service_tier" are deliberately absent. A member this
	// struct names is excluded from Capture, so declaring a field nothing
	// reads would delete it silently — worse than not knowing about it. Left
	// unnamed they ride along as extensions: replayed on an Interactions →
	// Interactions route, reported as unsupported on any other.
}

// usage uses total_* naming. Google's migration guide shows
// prompt_tokens/completion_tokens instead; recorded traffic does not, so this
// follows the recording.
type usage struct {
	TotalTokens        int `json:"total_tokens,omitempty"`
	TotalInputTokens   int `json:"total_input_tokens,omitempty"`
	TotalOutputTokens  int `json:"total_output_tokens,omitempty"`
	TotalThoughtTokens int `json:"total_thought_tokens,omitempty"`
	TotalCachedTokens  int `json:"total_cached_tokens,omitempty"`
	TotalToolUseTokens int `json:"total_tool_use_tokens,omitempty"`
}

type wireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
}

const (
	statusCompleted      = "completed"
	statusRequiresAction = "requires_action"
)

// --- streaming ------------------------------------------------------------

// streamEvent is one SSE payload. Events are discriminated by `event_type`
// *inside* the JSON, not by the SSE `event:` line — recorded traffic carries
// bare JSON objects, so relying on the line would drop every event.
type streamEvent struct {
	EventType string `json:"event_type"`
	EventID   string `json:"event_id,omitempty"`

	Index int        `json:"index,omitempty"`
	Step  *step      `json:"step,omitempty"`
	Delta *stepDelta `json:"delta,omitempty"`

	Interaction   *interactionResponse `json:"interaction,omitempty"`
	InteractionID string               `json:"interaction_id,omitempty"`
	Status        string               `json:"status,omitempty"`
	Error         *wireError           `json:"error,omitempty"`
}

// stepDelta is the payload of a step.delta event.
type stepDelta struct {
	Type string `json:"type"`

	// text
	Text        string       `json:"text,omitempty"`
	Annotations []annotation `json:"annotations,omitempty"`

	// thought_summary
	Content *thoughtSummaryItem `json:"content,omitempty"`

	// thought_signature, and the signature on a streamed function call
	Signature string `json:"signature,omitempty"`

	// arguments_delta. Despite the discriminator's name, the partial JSON
	// lives in `arguments` as a string. It is a fragment and must never be
	// parsed on its own.
	Arguments string `json:"arguments,omitempty"`
	ID        string `json:"id,omitempty"`

	// image / video deltas carry a whole payload; there is no byte streaming.
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// Event and delta discriminators.
const (
	evInteractionCreated = "interaction.created"
	evStepStart          = "step.start"
	evStepDelta          = "step.delta"
	evStepStop           = "step.stop"
	evCompleted          = "interaction.completed"
	evError              = "error"

	deltaText             = "text"
	deltaThoughtSummary   = "thought_summary"
	deltaThoughtSignature = "thought_signature"
	deltaArguments        = "arguments_delta"
	deltaImage            = "image"
)
