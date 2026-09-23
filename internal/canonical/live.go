package canonical

import "encoding/json"

// LiveMessage is one bidirectional session message. Unlike Event, which
// describes a one-way completion, these messages can arrive in either
// direction at any time. A Live session has its own codec and transport;
// buffering it into a Request/Response would lose interruption and tool
// response ordering.
type LiveMessage struct {
	Kind LiveKind

	// A setup is the only message that chooses a model. The router replaces
	// Model before the upstream codec encodes it.
	Model   string
	Options *LiveOptions

	// Real-time media is explicit, so audio is never mistaken for a document.
	Realtime *LiveRealtime

	Content       *LiveContent
	ToolResponses []LiveFunctionResponse
	ToolCalls     []ToolCall

	// A server message may carry multiple parts and transcripts at once.
	Output *LiveOutput
	Usage  *Usage
	// Unrecognised Live controls remain protocol-bound. This preserves new
	// vendor session signals without pretending they map to another dialect.
	NativeEvent *NativeContent

	CancelledToolIDs []string
	GoAwayTimeLeft   string
	ResumeHandle     string
	Resumable        bool
	// Provider-only fields are captured with the same protocol-bound rules as
	// the five stateless codecs; never forward them to another dialect.
	Extensions *Extensions
}

type LiveKind string

const (
	LiveSetup         LiveKind = "setup"
	LiveClientContent LiveKind = "client_content"
	LiveRealtimeInput LiveKind = "realtime_input"
	LiveToolResponse  LiveKind = "tool_response"
	LiveSetupComplete LiveKind = "setup_complete"
	LiveServerContent LiveKind = "server_content"
	LiveToolCall      LiveKind = "tool_call"
	LiveToolCancel    LiveKind = "tool_call_cancellation"
	LiveGoAway        LiveKind = "go_away"
	LiveResume        LiveKind = "session_resumption_update"
	LiveUsage         LiveKind = "usage"
	LiveNativeEvent   LiveKind = "native_event"
)

type LiveRealtime struct {
	Text           *string
	Audio          *Media
	Video          *Media
	AudioStreamEnd bool
	ActivityStart  bool
	ActivityEnd    bool
}

type LiveOptions struct {
	ResponseModalities  []string
	System              []ContentPart
	SystemRole          string
	HasSystem           bool
	InputTranscription  json.RawMessage
	OutputTranscription json.RawMessage
}

type LiveContent struct {
	Turns        []Message
	TurnComplete bool
}

type LiveOutput struct {
	Parts              []ContentPart
	ModelRole          string
	HasModelTurn       bool
	InputTranscript    string
	InputLanguage      string
	OutputTranscript   string
	OutputLanguage     string
	GenerationComplete bool
	TurnComplete       bool
	Interrupted        bool
	InteractionStatus  string
}

// LiveFunctionResponse carries the provider's structured tool result. It has the
// same meaning as ToolResult.Structured, but is a dedicated session message.
type LiveFunctionResponse struct {
	ID       string
	Name     string
	Response json.RawMessage
}
