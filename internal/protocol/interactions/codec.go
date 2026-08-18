// Package interactions implements Google's Interactions API as a Polyglot
// codec: Interactions <-> Canonical, in both directions.
//
// It is a separate protocol from `gemini`, not a mode of it, for the same
// reason OpenAI Responses is separate from OpenAI Chat Completions: the wire
// format is genuinely different. Requests carry a flat typed `input` array
// instead of contents/parts, replies are a `steps` timeline instead of
// candidates, tools are declared flat, citations are inline annotations rather
// than a separate groundingMetadata object, and the stream is a different
// event set.
//
// # Statelessness
//
// Interactions stores the conversation server-side by default: omit `store`
// and Google keeps it. Polyglot is stateless and keeps no copy of earlier
// turns, so it always sends `store: false` explicitly and passes the whole
// conversation in `input`. A `previous_interaction_id` arriving from a client
// is reported rather than honoured — the same stance the Responses codec takes
// on `store` and `previous_response_id`.
//
// Google requires that model-generated steps be replayed "exactly as
// received" when the conversation is not stored, which is what the per-step
// `signature` is for. That maps onto machinery Polyglot already has:
// `canonical.ToolCall.Signature` and `canonical.ReasoningMeta.Signature` carry
// it, and the existing cross-protocol envelope moves it through a client
// speaking something else.
//
// # Where this specification came from
//
// There is no Go SDK for this API — `google.golang.org/genai` has no
// Interactions client in any released version as of this writing — so these
// types were transcribed from the schema the official TypeScript provider
// validates against, cross-checked against its recorded traffic fixtures.
// Two things in that recording contradict Google's prose documentation, and
// the recording was followed:
//
//  1. Usage is `total_input_tokens` / `total_output_tokens` /
//     `total_thought_tokens`, not the `prompt_tokens` / `completion_tokens`
//     the migration guide shows.
//  2. Stream events are discriminated by an `event_type` member *inside* the
//     JSON payload. The documentation shows named SSE `event:` lines; the
//     recording has bare JSON objects, so keying off the line would drop
//     every event.
//
// Being unverifiable against an official SDK, this codec is held to the two
// test layers that are available — codec tests and integration tests over
// real HTTP against a mock upstream that replays the recorded shapes. It has
// deliberately not been given a fake "SDK compatibility" test, because a
// direct codec call dressed up as one would prove nothing.
package interactions

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
)

type Codec struct{}

func init() { protocol.Register(Codec{}) }

func (Codec) Name() protocol.Name { return protocol.GeminiInteractions }

// --- request: Interactions -> canonical -----------------------------------

func (Codec) DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error) {
	d = d.WithStage("decode:gemini-interactions")

	var in interactionRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid JSON body: %v", err)
	}
	if in.Model == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'model' is required")
	}
	if len(in.Input) == 0 || string(in.Input) == "null" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'input' is required")
	}

	req := &canonical.Request{
		Extensions:   protocol.Capture(protocol.GeminiInteractions, body, protocol.Top(interactionRequest{})),
		Model:        in.Model,
		Stream:       in.Stream,
		IncludeUsage: true,
	}
	if in.SystemInstruction != "" {
		req.System = append(req.System, canonical.Text(in.SystemInstruction))
	}

	// Server-side conversation state cannot be carried: Polyglot keeps no
	// history of its own, so a reference to a stored one resolves to nothing.
	if in.PreviousInteractionID != "" {
		d.Note("previous_interaction_id", canonical.FidelityUnsupported,
			"Polyglot is stateless and holds no stored interaction; send the full conversation in 'input' instead")
	}
	if in.Store != nil && *in.Store {
		d.Note("store", canonical.FidelityUnsupported,
			"server-side conversation storage is not used; Polyglot forwards store=false")
	}
	if in.Background != nil && *in.Background {
		d.Note("background", canonical.FidelityUnsupported,
			"background interactions are not supported; the request was made synchronously")
	}

	if gc := in.GenerationConfig; gc != nil {
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		req.MaxTokens = gc.MaxTokens
		req.Seed = gc.Seed
		req.Stop = gc.StopSequences
		if gc.ThinkingLevel != "" {
			req.Reasoning = &canonical.ReasoningConfig{
				Enabled: !strings.EqualFold(gc.ThinkingLevel, "off"),
				Effort:  thinkingToEffort(gc.ThinkingLevel),
				Visible: true,
			}
		}
	}
	if len(in.ResponseFormat) > 0 {
		req.ResponseFormat = decodeResponseFormat(in.ResponseFormat, d)
	}

	rawTools := protocol.RawArray(body, "tools")
	for i, t := range in.Tools {
		if t.Type != "function" {
			// google_search, code_execution and friends run inside Google.
			if i < len(rawTools) {
				req.NativeTools = req.NativeTools.Add(
					string(protocol.GeminiInteractions), t.Type, rawTools[i])
			}
			continue
		}
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	msgs, err := decodeInput(in.Input, d)
	if err != nil {
		return nil, err
	}
	req.Messages = msgs
	if len(req.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "'input' contains no usable steps")
	}
	return req, nil
}

// decodeInput handles the two shapes of `input`: a bare string, which is the
// one-shot form, or the array of steps a conversation uses.
func decodeInput(raw json.RawMessage, d *canonical.Diagnostics) ([]canonical.Message, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'input' string: %v", err)
		}
		if s == "" {
			return nil, nil
		}
		return []canonical.Message{{
			Role:    canonical.RoleUser,
			Content: []canonical.ContentPart{canonical.Text(s)},
		}}, nil
	}

	// A single step object is also accepted, which is how a function result
	// is handed back on its own.
	if strings.HasPrefix(trimmed, "{") {
		var one step
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'input' object: %v", err)
		}
		return stepsToMessages([]step{one}, d), nil
	}

	var steps []step
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'input' array: %v", err)
	}
	return stepsToMessages(steps, d), nil
}

// stepsToMessages folds a step timeline into canonical messages.
//
// Consecutive model-side steps belong to one assistant turn: a thought, the
// function call it led to and any text all arrive as separate steps but are
// one message in every other protocol.
func stepsToMessages(steps []step, d *canonical.Diagnostics) []canonical.Message {
	var out []canonical.Message

	// appendTo merges into the trailing message when it has the same role, so
	// a model turn spread over several steps does not become several turns.
	appendTo := func(role canonical.Role, parts ...canonical.ContentPart) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, parts...)
			return
		}
		out = append(out, canonical.Message{Role: role, Content: parts})
	}

	for i, s := range steps {
		where := fmt.Sprintf("input[%d]", i)
		switch {
		case s.Type == stepUserInput:
			appendTo(canonical.RoleUser, decodeContent(s.Content, d, where)...)

		case s.Type == stepModelOutput:
			appendTo(canonical.RoleAssistant, decodeContent(s.Content, d, where)...)

		case s.Type == stepThought:
			part := canonical.ContentPart{Type: canonical.PartReasoning, Text: summaryText(s.Summary)}
			if sig := deref(s.Signature); sig != "" {
				// The replay token. Google rejects a later turn that has lost
				// it when the conversation is not stored server-side.
				part.Reasoning = &canonical.ReasoningMeta{Signature: sig}
			}
			appendTo(canonical.RoleAssistant, part)

		case s.Type == stepFunctionCall:
			args := s.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			appendTo(canonical.RoleAssistant, canonical.ContentPart{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID:        s.ID,
					Name:      s.Name,
					Arguments: args,
					Signature: deref(s.Signature),
				},
			})

		case s.Type == stepFunctionResult:
			appendTo(canonical.RoleTool, canonical.ContentPart{
				Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{
					ToolCallID: s.CallID,
					Name:       s.Name,
					Content:    resultContent(s.Result, d, where),
					Structured: structuredResult(s.Result),
					IsError:    s.IsError,
				},
			})

		case isBuiltinStep(s.Type):
			// A record that Google ran one of its own tools. There is nothing
			// for Polyglot to relay and no canonical place to keep it, so it
			// is reported rather than dropped in silence. Replaying a history
			// containing these to Interactions may therefore lose reasoning
			// continuity — see the package comment.
			d.Note(where, canonical.FidelityUnsupported,
				"a %s step was not carried: it records a tool Google ran itself", s.Type)

		default:
			d.Note(where, canonical.FidelityUnsupported,
				"unknown step type %q was dropped", s.Type)
		}
	}
	return out
}

func decodeContent(blocks []contentBlock, d *canonical.Diagnostics, where string) []canonical.ContentPart {
	var out []canonical.ContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, canonical.Text(b.Text))
			}
		case "image", "video":
			m := blockMedia(b)
			if m == nil {
				d.Note(where, canonical.FidelityUnsupported, "an %s block carried no data or uri", b.Type)
				continue
			}
			kind, ok := protocol.ClassifyMedia(m.MIMEType)
			if !ok {
				d.Note(where, canonical.FidelityUnsupported,
					"%s content is not converted yet and was not forwarded", m.MIMEType)
				continue
			}
			out = append(out, canonical.ContentPart{Type: kind, Media: m})
		default:
			d.Note(where, canonical.FidelityUnsupported, "unknown content block type %q was dropped", b.Type)
		}
	}
	return out
}

func blockMedia(b contentBlock) *canonical.Media {
	switch {
	case b.Data != "":
		return &canonical.Media{MIMEType: b.MIMEType, Data: b.Data, Detail: b.Resolution}
	case b.URI != "":
		// A Google Files URI is provider-bound; anything else is a plain
		// remote reference the target may or may not be able to fetch.
		if strings.Contains(b.URI, "generativelanguage.googleapis.com") {
			return &canonical.Media{
				MIMEType: b.MIMEType,
				FileID:   b.URI,
				Provider: string(protocol.GeminiInteractions),
			}
		}
		return &canonical.Media{MIMEType: b.MIMEType, URL: b.URI, Detail: b.Resolution}
	}
	return nil
}

// resultContent reads a function result payload, which is either an array of
// content blocks or an opaque JSON value.
// encodeResult writes a function_result payload.
//
// A result that arrived as JSON goes back out as JSON. Flattening it into a
// text block would hand the model a *string containing JSON* where it had been
// given data — the value survives, but the model has to parse its own tool
// output back out of prose. Only a result that was genuinely text becomes a
// text block.
func encodeResult(tr *canonical.ToolResult) (json.RawMessage, error) {
	if len(tr.Structured) > 0 && json.Valid(tr.Structured) {
		return tr.Structured, nil
	}
	b, err := json.Marshal([]contentBlock{{Type: "text", Text: joinText(tr.Content)}})
	if err != nil {
		return nil, fmt.Errorf("encode function result: %w", err)
	}
	return b, nil
}

func resultContent(raw json.RawMessage, d *canonical.Diagnostics, where string) []canonical.ContentPart {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, "[") {
		var blocks []contentBlock
		if err := json.Unmarshal(raw, &blocks); err == nil {
			return decodeContent(blocks, d, where)
		}
	}
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, `"`) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []canonical.ContentPart{canonical.Text(s)}
		}
	}
	// An object or a scalar: keep it verbatim as text so nothing is lost.
	return []canonical.ContentPart{canonical.Text(string(raw))}
}

// structuredResult keeps the payload as JSON when it arrived as JSON, so a
// route back to Interactions can send data rather than a description of it.
//
// A content-block array is excluded: resultContent already turns those into
// canonical parts, and the encoder rebuilds them identically, so keeping a
// second copy would only give the two ways to disagree.
func structuredResult(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || !json.Valid(raw) {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, `"`) {
		return nil
	}
	return raw
}

func summaryText(items []thoughtSummaryItem) string {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(it.Text)
	}
	return sb.String()
}

func decodeResponseFormat(f []responseFormat, d *canonical.Diagnostics) *canonical.ResponseFormat {
	if len(f) > 1 {
		d.Note("response_format", canonical.FidelityLossy,
			"%d response formats were requested; only the first is carried", len(f))
	}
	first := f[0]
	switch {
	case first.MIMEType == "application/json" && len(first.Schema) > 0:
		return &canonical.ResponseFormat{Type: canonical.FormatJSONSchema, Schema: first.Schema}
	case first.MIMEType == "application/json":
		return &canonical.ResponseFormat{Type: canonical.FormatJSONObject}
	default:
		return &canonical.ResponseFormat{Type: canonical.FormatText}
	}
}

func thinkingToEffort(level string) canonical.ReasoningEffort {
	switch strings.ToLower(level) {
	case "low":
		return canonical.EffortLow
	case "medium", "standard":
		return canonical.EffortMedium
	case "high", "max":
		return canonical.EffortHigh
	}
	return ""
}

func effortToThinking(e canonical.ReasoningEffort) string {
	switch e {
	case canonical.EffortMinimal, canonical.EffortLow:
		return "low"
	case canonical.EffortMedium:
		return "medium"
	case canonical.EffortHigh:
		return "high"
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- request: canonical -> Interactions -----------------------------------

func (Codec) EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:gemini-interactions")
	protocol.NoteResponseState(req, protocol.GeminiInteractions, d)
	protocol.NoteCacheHints(req, d)
	protocol.NoteTextSignatures(req, d)

	out := interactionRequest{
		Model:  req.Model,
		Stream: req.Stream,
		// Always explicit. The field defaults to true when absent, and a
		// gateway that keeps no history must not have the provider keeping one
		// on its behalf.
		Store: canonical.Ptr(false),
	}
	if len(req.System) > 0 {
		out.SystemInstruction = joinText(req.System)
	}

	gc := generationConfig{
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		MaxTokens:     req.MaxTokens,
		Seed:          req.Seed,
		StopSequences: req.Stop,
	}
	if req.Reasoning != nil {
		if !req.Reasoning.Enabled {
			gc.ThinkingLevel = "off"
		} else if lvl := effortToThinking(req.Reasoning.Effort); lvl != "" {
			gc.ThinkingLevel = lvl
		}
		if req.Reasoning.BudgetTokens != nil {
			d.Note("reasoning.budget_tokens", canonical.FidelityLossy,
				"a token budget was expressed as thinking_level %q: Interactions has no budget field",
				orDefault(gc.ThinkingLevel, "default"))
		}
	}
	if gc.Temperature != nil || gc.TopP != nil || gc.MaxTokens != nil || gc.Seed != nil ||
		len(gc.StopSequences) > 0 || gc.ThinkingLevel != "" {
		out.GenerationConfig = &gc
	}
	if req.TopK != nil {
		d.Note("top_k", canonical.FidelityUnsupported, "Interactions has no top_k parameter")
	}
	if req.N != nil && *req.N > 1 {
		d.Note("n", canonical.FidelityUnsupported, "Interactions returns a single interaction per request")
	}

	if rf := req.ResponseFormat; rf != nil {
		switch rf.Type {
		case canonical.FormatJSONSchema:
			out.ResponseFormat = []responseFormat{{
				Type: "text", MIMEType: "application/json", Schema: rf.Schema,
			}}
		case canonical.FormatJSONObject:
			out.ResponseFormat = []responseFormat{{Type: "text", MIMEType: "application/json"}}
		case canonical.FormatText:
			out.ResponseFormat = []responseFormat{{Type: "text"}}
		}
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, tool{
			Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}
	if req.ToolChoice != nil && req.ToolChoice.Mode != canonical.ToolChoiceAuto {
		d.Note("tool_choice", canonical.FidelityUnsupported,
			"Interactions has no tool choice control; the model decides")
	}

	steps, err := encodeInput(req.Messages, d)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request has no messages")
	}
	rawInput, err := json.Marshal(steps)
	if err != nil {
		return nil, fmt.Errorf("encode interactions input: %w", err)
	}
	out.Input = rawInput

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode interactions request: %w", err)
	}
	b = protocol.MergeNativeTools(protocol.GeminiInteractions, req.NativeTools, b, d)
	return protocol.Merge(protocol.GeminiInteractions, req.Extensions, b, d), nil
}

// encodeInput turns canonical messages back into a step timeline.
//
// Model-side steps carry their signature back verbatim, which is what lets a
// conversation continue when the provider is holding no state for it.
func encodeInput(msgs []canonical.Message, d *canonical.Diagnostics) ([]step, error) {
	var out []step

	for i, m := range msgs {
		where := fmt.Sprintf("messages[%d]", i)
		switch m.Role {
		case canonical.RoleTool:
			for _, p := range m.Content {
				if p.Type != canonical.PartToolResult || p.ToolResult == nil {
					continue
				}
				result, err := encodeResult(p.ToolResult)
				if err != nil {
					return nil, err
				}
				out = append(out, step{
					Type:    stepFunctionResult,
					CallID:  p.ToolResult.ToolCallID,
					Name:    p.ToolResult.Name,
					Result:  result,
					IsError: p.ToolResult.IsError,
				})
			}
			continue

		case canonical.RoleSystem:
			// Hoisted into system_instruction by the caller.
			continue
		}

		var content []contentBlock
		for _, p := range m.Content {
			switch p.Type {
			case canonical.PartText:
				if p.Text != "" {
					content = append(content, contentBlock{Type: "text", Text: p.Text})
				}
			case canonical.PartImage, canonical.PartFile:
				if b := encodeMediaBlock(p, d, where); b != nil {
					content = append(content, *b)
				}
			case canonical.PartNative:
				protocol.NoteNativeContent(p, protocol.GeminiInteractions, where+".content", d)
			}
		}

		if m.Role == canonical.RoleUser {
			if len(content) > 0 {
				out = append(out, step{Type: stepUserInput, Content: content})
			}
			// A user turn may also carry tool results when the conversation
			// began on a protocol that models them that way.
			for _, p := range m.Content {
				if p.Type != canonical.PartToolResult || p.ToolResult == nil {
					continue
				}
				result, err := encodeResult(p.ToolResult)
				if err != nil {
					return nil, err
				}
				out = append(out, step{
					Type: stepFunctionResult, CallID: p.ToolResult.ToolCallID,
					Name: p.ToolResult.Name, Result: result, IsError: p.ToolResult.IsError,
				})
			}
			continue
		}

		// An assistant turn becomes several steps, in the order the model
		// produced them: thinking, then the call it decided on, then text.
		for _, p := range m.Content {
			if p.Type != canonical.PartReasoning {
				continue
			}
			th := step{Type: stepThought}
			if p.Text != "" {
				th.Summary = []thoughtSummaryItem{{Type: "text", Text: p.Text}}
			}
			if p.Reasoning != nil && p.Reasoning.Signature != "" {
				th.Signature = canonical.Ptr(p.Reasoning.Signature)
			} else {
				// Without the token Google cannot resume its own reasoning.
				// Sending the thought anyway is still better than dropping the
				// turn, but the discontinuity is recorded.
				d.Note(where+".reasoning", canonical.FidelityLossy,
					"assistant reasoning was replayed without a signature: it did not come from Interactions, "+
						"so reasoning continuity is lost")
			}
			out = append(out, th)
		}
		if len(content) > 0 {
			out = append(out, step{Type: stepModelOutput, Content: content})
		}
		for _, p := range m.Content {
			if p.Type != canonical.PartToolCall || p.ToolCall == nil {
				continue
			}
			args := p.ToolCall.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			fc := step{
				Type:      stepFunctionCall,
				ID:        orDefault(p.ToolCall.ID, "fc_"+idgen.New()),
				Name:      p.ToolCall.Name,
				Arguments: args,
			}
			if p.ToolCall.Signature != "" {
				fc.Signature = canonical.Ptr(p.ToolCall.Signature)
			} else {
				d.Note(where+".tool_call.signature", canonical.FidelityLossy,
					"function call %q was replayed without a signature: it did not come from Interactions, "+
						"which may reject the turn", p.ToolCall.Name)
			}
			out = append(out, fc)
		}
	}
	return out, nil
}

// encodeMediaBlock renders an attachment as an Interactions content block.
func encodeMediaBlock(p canonical.ContentPart, d *canonical.Diagnostics, where string) *contentBlock {
	m := p.Media
	if m == nil {
		return nil
	}
	// Interactions has no separate document block: a PDF rides in the same
	// carrier with its own mime_type, which is what identifies it.
	const kind = "image"
	switch {
	case m.Inline():
		return &contentBlock{Type: kind, Data: m.Data, MIMEType: m.MIMEType, Resolution: resolution(m.Detail)}
	case protocol.BoundMediaUsable(protocol.GeminiInteractions, m):
		return &contentBlock{Type: kind, URI: m.FileID, MIMEType: m.MIMEType}
	case m.Bound():
		protocol.MediaNote(d, where, m,
			"it is a file handle issued by "+m.Provider+", which Interactions cannot resolve")
		return nil
	case m.Remote():
		return &contentBlock{Type: kind, URI: m.URL, MIMEType: m.MIMEType, Resolution: resolution(m.Detail)}
	}
	protocol.MediaNote(d, where, m, "it carried no data, url or file id")
	return nil
}

// resolution maps a detail hint onto Interactions' vocabulary, which is the
// same idea under another name.
func resolution(detail string) string {
	switch strings.ToLower(detail) {
	case "low":
		return "low"
	case "high":
		return "high"
	case "auto", "":
		return ""
	}
	return ""
}

// --- response: Interactions -> canonical ----------------------------------

func (Codec) DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error) {
	d = d.WithStage("decode:gemini-interactions")

	var in interactionResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned invalid JSON: %v", err)
	}
	if in.Error != nil && in.Error.Message != "" {
		return nil, canonical.Errorf(canonical.ErrUpstream, "%s", in.Error.Message)
	}
	if in.Steps == nil {
		return nil, canonical.Errorf(canonical.ErrUpstream,
			"upstream reply has no 'steps' array; is this provider really speaking the Interactions API?")
	}

	resp := &canonical.Response{
		ID:         in.ID,
		Model:      in.Model,
		Created:    parseCreated(in.Created),
		Extensions: protocol.Capture(protocol.GeminiInteractions, body, protocol.Top(interactionResponse{})),
	}
	if in.Usage != nil {
		resp.Usage = canonical.Usage{
			InputTokens:       in.Usage.TotalInputTokens,
			OutputTokens:      in.Usage.TotalOutputTokens,
			ReasoningTokens:   in.Usage.TotalThoughtTokens,
			CachedInputTokens: in.Usage.TotalCachedTokens,
		}
	}

	msg := canonical.Message{Role: canonical.RoleAssistant}
	sawToolCall := false
	for i, s := range in.Steps {
		where := fmt.Sprintf("steps[%d]", i)
		switch {
		case s.Type == stepModelOutput:
			msg.Content = append(msg.Content, decodeContent(s.Content, d, where)...)
		case s.Type == stepThought:
			part := canonical.ContentPart{Type: canonical.PartReasoning, Text: summaryText(s.Summary)}
			if sig := deref(s.Signature); sig != "" {
				part.Reasoning = &canonical.ReasoningMeta{Signature: sig}
			}
			msg.Content = append(msg.Content, part)
		case s.Type == stepFunctionCall:
			sawToolCall = true
			args := s.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID: s.ID, Name: s.Name, Arguments: args, Signature: deref(s.Signature),
				},
			})
		case s.Type == stepUserInput:
			// Only present on a full-timeline fetch; it is the echo of what
			// was sent, not part of the reply.
		case isBuiltinStep(s.Type):
			d.Note(where, canonical.FidelitySemantic,
				"a %s step records a tool Google ran itself; its result is reflected in the reply text", s.Type)
		default:
			d.Note(where, canonical.FidelityUnsupported, "unknown step type %q was dropped", s.Type)
		}
	}
	resp.Message = msg

	switch {
	case in.Status == statusRequiresAction || sawToolCall:
		resp.FinishReason = canonical.FinishToolCalls
	case in.Status == statusCompleted:
		resp.FinishReason = canonical.FinishStop
	default:
		resp.FinishReason = canonical.FinishStop
	}
	return resp, nil
}

// --- response: canonical -> Interactions ----------------------------------

func (Codec) EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:gemini-interactions")
	protocol.NoteCacheWrite(resp.Usage, d)

	model := resp.Model
	if model == "" && req != nil {
		model = req.Model
	}
	created := resp.Created
	if created.IsZero() {
		created = time.Now()
	}
	out := interactionResponse{
		ID:      orDefault(resp.ID, "v1_"+idgen.New()),
		Object:  "interaction",
		Model:   model,
		Status:  statusCompleted,
		Created: created.UTC().Format(time.RFC3339Nano),
		Usage: &usage{
			TotalInputTokens:   resp.Usage.InputTokens,
			TotalOutputTokens:  resp.Usage.OutputTokens,
			TotalThoughtTokens: resp.Usage.ReasoningTokens,
			TotalCachedTokens:  resp.Usage.CachedInputTokens,
			TotalTokens: resp.Usage.InputTokens + resp.Usage.OutputTokens +
				resp.Usage.ReasoningTokens,
		},
	}

	var content []contentBlock
	for _, p := range resp.Message.Content {
		switch p.Type {
		case canonical.PartReasoning:
			th := step{Type: stepThought}
			if p.Text != "" {
				th.Summary = []thoughtSummaryItem{{Type: "text", Text: p.Text}}
			}
			if p.Reasoning != nil && p.Reasoning.Signature != "" {
				th.Signature = canonical.Ptr(p.Reasoning.Signature)
			}
			out.Steps = append(out.Steps, th)
		case canonical.PartText:
			if p.Text != "" {
				content = append(content, contentBlock{Type: "text", Text: p.Text})
			}
		case canonical.PartImage, canonical.PartFile:
			if b := encodeMediaBlock(p, d, "message.content"); b != nil {
				content = append(content, *b)
			}
		case canonical.PartNative:
			protocol.NoteNativeContent(p, protocol.GeminiInteractions, "message.content", d)
		}
	}
	if len(content) > 0 {
		out.Steps = append(out.Steps, step{Type: stepModelOutput, Content: content})
	}
	for _, tc := range resp.ToolCalls() {
		args := tc.Arguments
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		s := step{
			Type:      stepFunctionCall,
			ID:        orDefault(tc.ID, "fc_"+idgen.New()),
			Name:      tc.Name,
			Arguments: args,
		}
		if tc.Signature != "" {
			s.Signature = canonical.Ptr(tc.Signature)
		}
		out.Steps = append(out.Steps, s)
		out.Status = statusRequiresAction
	}
	if out.Steps == nil {
		out.Steps = []step{}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode interactions response: %w", err)
	}
	return protocol.Merge(protocol.GeminiInteractions, resp.Extensions, b, d), nil
}

func (Codec) EncodeError(err *canonical.Error) []byte {
	out := struct {
		Error wireError `json:"error"`
	}{Error: wireError{
		Code:    string(err.Type),
		Message: err.Message,
		Status:  string(err.Type),
	}}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return []byte(`{"error":{"message":"internal error"}}`)
	}
	return b
}

func joinText(parts []canonical.ContentPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == canonical.PartText {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// parseCreated reads the RFC 3339 response timestamp, falling back to the
// local clock when an upstream omits it or spells it differently. It is
// descriptive metadata, never worth failing a response over.
func parseCreated(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}
