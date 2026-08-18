package canonical

import "encoding/json"

// EventType enumerates Polyglot's protocol-neutral streaming events. Every
// upstream stream format is decoded into this sequence, and every client
// protocol is encoded from it.
type EventType string

const (
	// EventMessageStart is emitted once, before any content.
	EventMessageStart EventType = "message.start"

	EventTextDelta      EventType = "text.delta"
	EventReasoningDelta EventType = "reasoning.delta"
	// EventNative carries an opaque provider SSE event for same-protocol
	// forwarding. Cross-protocol gateways record and omit it.
	EventNative EventType = "native"

	// EventToolCallStart announces a tool call; Arguments may still be empty.
	EventToolCallStart EventType = "tool_call.start"
	// EventToolCallDelta carries a raw fragment of the argument JSON. A
	// fragment is NOT required to be valid JSON on its own.
	EventToolCallDelta EventType = "tool_call.arguments.delta"
	EventToolCallEnd   EventType = "tool_call.end"

	EventUsage EventType = "usage"
	// EventMessageEnd is emitted once, carrying the finish reason.
	EventMessageEnd EventType = "message.end"
	EventError      EventType = "error"
)

// Event is a single item in a canonical stream. Index identifies the content
// block the event belongs to, so interleaved text/reasoning/tool blocks stay
// distinguishable across protocols.
type Event struct {
	Type  EventType `json:"type"`
	Index int       `json:"index"`

	// ID and Model are set on EventMessageStart.
	ID    string `json:"id,omitempty"`
	Model string `json:"model,omitempty"`

	// Text carries text.delta and reasoning.delta payloads.
	Text string `json:"text,omitempty"`

	// Signature is a replay token bound to a text delta — Gemini closes a
	// thinking block on the text part that follows it. Reasoning and tool
	// calls carry theirs in Reasoning and ToolCallSignature.
	Signature string `json:"signature,omitempty"`

	// Reasoning carries signature/redacted metadata on reasoning deltas.
	Reasoning *ReasoningMeta `json:"reasoning,omitempty"`

	// ToolCallID / ToolName are set on tool_call.start and repeated on
	// tool_call.end so encoders never need to keep their own index map.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	// ArgumentsDelta is a raw fragment of the tool argument JSON.
	ArgumentsDelta string `json:"arguments_delta,omitempty"`
	// ToolCallSignature carries ToolCall.Signature on tool_call.start/end.
	ToolCallSignature string `json:"tool_call_signature,omitempty"`

	Usage        *Usage       `json:"usage,omitempty"`
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	Error        *Error       `json:"error,omitempty"`
	Native       *NativeEvent `json:"native,omitempty"`
}

type NativeEvent struct {
	Protocol string          `json:"protocol"`
	Name     string          `json:"name"`
	Raw      json.RawMessage `json:"raw"`
}

// Accumulator rebuilds a complete Response from a canonical event stream. It
// is used both to serve non-streaming clients from a streaming upstream and to
// produce request logs without touching the database per delta.
type Accumulator struct {
	resp   Response
	blocks map[int]*accBlock
	order  []int
}

type accBlock struct {
	kind      PartType
	sb        []byte
	signature string
	meta      ReasoningMeta
	tc        ToolCall
}

func NewAccumulator() *Accumulator {
	return &Accumulator{blocks: map[int]*accBlock{}}
}

func (a *Accumulator) block(i int, kind PartType) *accBlock {
	b, ok := a.blocks[i]
	if !ok {
		b = &accBlock{kind: kind}
		a.blocks[i] = b
		a.order = append(a.order, i)
	}
	return b
}

func (a *Accumulator) Add(ev *Event) {
	switch ev.Type {
	case EventMessageStart:
		if ev.ID != "" {
			a.resp.ID = ev.ID
		}
		if ev.Model != "" {
			a.resp.Model = ev.Model
		}
	case EventTextDelta:
		b := a.block(ev.Index, PartText)
		b.sb = append(b.sb, ev.Text...)
		if ev.Signature != "" {
			b.signature = ev.Signature
		}
	case EventReasoningDelta:
		b := a.block(ev.Index, PartReasoning)
		b.sb = append(b.sb, ev.Text...)
		if ev.Reasoning != nil {
			if ev.Reasoning.Signature != "" {
				b.meta.Signature = ev.Reasoning.Signature
			}
			if ev.Reasoning.Redacted != "" {
				b.meta.Redacted = ev.Reasoning.Redacted
			}
			if ev.Reasoning.ID != "" {
				b.meta.ID = ev.Reasoning.ID
			}
		}
	case EventToolCallStart:
		b := a.block(ev.Index, PartToolCall)
		b.tc.ID = ev.ToolCallID
		b.tc.Name = ev.ToolName
		if ev.ToolCallSignature != "" {
			b.tc.Signature = ev.ToolCallSignature
		}
		b.sb = append(b.sb, ev.ArgumentsDelta...)
	case EventToolCallDelta:
		b := a.block(ev.Index, PartToolCall)
		b.sb = append(b.sb, ev.ArgumentsDelta...)
	case EventToolCallEnd:
		b := a.block(ev.Index, PartToolCall)
		if ev.ToolCallID != "" {
			b.tc.ID = ev.ToolCallID
		}
		if ev.ToolName != "" {
			b.tc.Name = ev.ToolName
		}
		if ev.ToolCallSignature != "" {
			b.tc.Signature = ev.ToolCallSignature
		}
	case EventUsage:
		if ev.Usage != nil {
			a.resp.Usage = *ev.Usage
		}
	case EventMessageEnd:
		if ev.FinishReason != "" {
			a.resp.FinishReason = ev.FinishReason
		}
		if ev.Usage != nil {
			a.resp.Usage = *ev.Usage
		}
	}
}

// Response materialises the accumulated message.
func (a *Accumulator) Response() *Response {
	r := a.resp
	r.Message.Role = RoleAssistant
	r.Message.Content = nil
	for _, i := range a.order {
		b := a.blocks[i]
		switch b.kind {
		case PartText:
			r.Message.Content = append(r.Message.Content, ContentPart{
				Type: PartText, Text: string(b.sb), Signature: b.signature,
			})
		case PartReasoning:
			p := ContentPart{Type: PartReasoning, Text: string(b.sb)}
			if b.meta != (ReasoningMeta{}) {
				m := b.meta
				p.Reasoning = &m
			}
			r.Message.Content = append(r.Message.Content, p)
		case PartToolCall:
			tc := b.tc
			args := b.sb
			if len(args) == 0 {
				args = []byte("{}")
			}
			tc.Arguments = append([]byte(nil), args...)
			r.Message.Content = append(r.Message.Content, ContentPart{Type: PartToolCall, ToolCall: &tc})
		}
	}
	return &r
}
