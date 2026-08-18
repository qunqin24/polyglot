// Package anthropic implements the Anthropic Messages protocol as a Polyglot
// codec: Anthropic <-> Canonical, in both directions.
package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
)

// defaultMaxTokens is used when a canonical request carries no limit.
// Anthropic requires max_tokens, unlike the other two protocols, so a value
// has to be invented — and the invention is recorded as a conversion note.
const defaultMaxTokens = 4096

// minThinkingBudget is Anthropic's floor for extended thinking.
const minThinkingBudget = 1024

type Codec struct{}

func init() { protocol.Register(Codec{}) }

func (Codec) Name() protocol.Name { return protocol.Anthropic }

// --- request: Anthropic -> canonical --------------------------------------

func (Codec) DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error) {
	d = d.WithStage("decode:anthropic")

	var in messagesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid JSON body: %v", err)
	}
	if in.Model == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'model' is required")
	}
	if len(in.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'messages' must not be empty")
	}

	req := &canonical.Request{
		Extensions:  protocol.Capture(protocol.Anthropic, body, protocol.Top(messagesRequest{})),
		Model:       in.Model,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		TopK:        in.TopK,
		Stop:        in.StopSequences,
		Stream:      in.Stream,
		// Anthropic always reports usage, so the canonical request is marked
		// as wanting it; protocols that make it opt-in will honour that.
		IncludeUsage: true,
	}
	if in.MaxTokens > 0 {
		req.MaxTokens = &in.MaxTokens
	}
	if in.Metadata != nil && in.Metadata.UserID != "" {
		req.User = in.Metadata.UserID
	}
	if in.Thinking != nil && in.Thinking.Type == "enabled" {
		rc := &canonical.ReasoningConfig{Enabled: true, Visible: true}
		if in.Thinking.BudgetTokens > 0 {
			b := in.Thinking.BudgetTokens
			rc.BudgetTokens = &b
		}
		req.Reasoning = rc
	}

	system, err := decodeSystem(in.System, d)
	if err != nil {
		return nil, err
	}
	req.System = system

	rawTools := protocol.RawArray(body, "tools")
	for i, t := range in.Tools {
		if t.Type != "" && !strings.HasPrefix(t.Type, "custom") {
			// Server-side tools (web_search, computer, text_editor, ...) run
			// inside Anthropic. No other protocol can express them, but an
			// Anthropic upstream can still run them.
			if i < len(rawTools) {
				req.NativeTools = req.NativeTools.Add(string(protocol.Anthropic), t.Name, rawTools[i])
			}
			continue
		}
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			Cache:       t.CacheControl.hint(),
		})
	}
	if in.ToolChoice != nil {
		tc := &canonical.ToolChoice{ParallelDisabled: in.ToolChoice.DisableParallelToolUse}
		switch in.ToolChoice.Type {
		case "auto":
			tc.Mode = canonical.ToolChoiceAuto
		case "any":
			tc.Mode = canonical.ToolChoiceRequired
		case "none":
			tc.Mode = canonical.ToolChoiceNone
		case "tool":
			tc.Mode = canonical.ToolChoiceSpecific
			tc.Name = in.ToolChoice.Name
		default:
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "unsupported tool_choice.type %q", in.ToolChoice.Type)
		}
		req.ToolChoice = tc
	}

	for i, m := range in.Messages {
		msg, err := decodeMessage(m, d, i)
		if err != nil {
			return nil, err
		}
		req.Messages = append(req.Messages, msg)
	}
	return req, nil
}

func decodeSystem(raw json.RawMessage, d *canonical.Diagnostics) ([]canonical.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'system': %v", err)
		}
		if s == "" {
			return nil, nil
		}
		return []canonical.ContentPart{canonical.Text(s)}, nil
	}
	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'system': %v", err)
	}
	var out []canonical.ContentPart
	for _, b := range blocks {
		before := len(out)
		if b.Type == "text" && b.Text != "" {
			out = append(out, canonical.Text(b.Text))
		} else if b.Type != "text" {
			d.Note("system", canonical.FidelityUnsupported, "system block of type %q was dropped", b.Type)
		}
		attachCache(out, before, b.CacheControl)
	}
	return out, nil
}

func decodeMessage(m wireMessage, d *canonical.Diagnostics, idx int) (canonical.Message, error) {
	out := canonical.Message{Role: canonical.RoleUser}
	if m.Role == "assistant" {
		out.Role = canonical.RoleAssistant
	}

	parts, err := decodeBlocks(m.Content, d, fmt.Sprintf("messages[%d]", idx))
	if err != nil {
		return out, err
	}
	out.Content = parts

	// Anthropic carries tool results inside a user turn. Canonical keeps a
	// dedicated tool role, which is what OpenAI needs; a turn that is nothing
	// but tool results becomes a tool turn.
	if out.Role == canonical.RoleUser && len(parts) > 0 && allToolResults(parts) {
		out.Role = canonical.RoleTool
	}
	return out, nil
}

func allToolResults(parts []canonical.ContentPart) bool {
	for _, p := range parts {
		if p.Type != canonical.PartToolResult {
			return false
		}
	}
	return true
}

func decodeBlocks(raw json.RawMessage, d *canonical.Diagnostics, where string) ([]canonical.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "%s: invalid content: %v", where, err)
		}
		if s == "" {
			return nil, nil
		}
		return []canonical.ContentPart{canonical.Text(s)}, nil
	}

	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "%s: invalid content: %v", where, err)
	}
	var out []canonical.ContentPart
	for _, b := range blocks {
		before := len(out)
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, canonical.Text(b.Text))
			}
		case "thinking":
			p := canonical.ContentPart{Type: canonical.PartReasoning, Text: b.Thinking}
			if b.Signature != "" {
				p.Reasoning = &canonical.ReasoningMeta{Signature: b.Signature}
			}
			out = append(out, p)
		case "redacted_thinking":
			out = append(out, canonical.ContentPart{
				Type:      canonical.PartReasoning,
				Reasoning: &canonical.ReasoningMeta{Redacted: b.Data},
			})
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			out = append(out, canonical.ContentPart{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID: b.ID, Name: b.Name, Arguments: input,
					Signature: b.ExtraContent.Signature(),
				},
			})
		case "tool_result":
			content, err := decodeBlocks(b.Content, d, where+".tool_result")
			if err != nil {
				return nil, err
			}
			out = append(out, canonical.ContentPart{
				Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{
					ToolCallID: b.ToolUseID,
					Content:    content,
					IsError:    b.IsError,
				},
			})
		case "image", "document":
			m := sourceMedia(b.Source)
			if m == nil {
				d.Note(where, canonical.FidelityUnsupported,
					"a %s block carried no usable source", b.Type)
				continue
			}
			if m.Filename == "" {
				m.Filename = b.Title
			}
			kind, ok := protocol.ClassifyMedia(m.MIMEType)
			if b.Type == "image" {
				kind, ok = canonical.PartImage, true
			}
			if !ok {
				d.Note(where, canonical.FidelityUnsupported,
					"%s content is not converted yet and was not forwarded", m.MIMEType)
				continue
			}
			out = append(out, canonical.ContentPart{Type: kind, Media: m})
		case "search_result":
			out = append(out, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(protocol.Anthropic), Type: b.Type,
					Raw: append(json.RawMessage(nil), b.Raw...)}})
		default:
			out = append(out, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(protocol.Anthropic), Type: b.Type,
					Raw: append(json.RawMessage(nil), b.Raw...)}})
		}
		attachCache(out, before, b.CacheControl)
	}
	return out, nil
}

// attachCache marks the part a block produced as a cache breakpoint. Anthropic
// puts the marker on the block; canonical puts it on the part that block
// became, which keeps its position in the prompt — and the position is the
// whole meaning of a breakpoint, since it says where the cacheable prefix ends.
func attachCache(parts []canonical.ContentPart, before int, c *cacheControl) {
	if c == nil || len(parts) == before {
		return
	}
	parts[len(parts)-1].Cache = c.hint()
}

// --- request: canonical -> Anthropic --------------------------------------

func (Codec) EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:anthropic")
	protocol.NoteResponseState(req, protocol.Anthropic, d)

	out := messagesRequest{
		Model:         req.Model,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		TopK:          req.TopK,
		StopSequences: req.Stop,
		Stream:        req.Stream,
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		out.MaxTokens = *req.MaxTokens
	} else {
		out.MaxTokens = defaultMaxTokens
		d.Note("max_tokens", canonical.FidelitySemantic,
			"Anthropic requires max_tokens; the request had none so %d was used", defaultMaxTokens)
	}
	if req.User != "" {
		out.Metadata = &wireMetadata{UserID: req.User}
	}
	if req.PresencePenalty != nil {
		d.Note("presence_penalty", canonical.FidelityUnsupported, "Anthropic has no presence_penalty parameter")
	}
	if req.FrequencyPenalty != nil {
		d.Note("frequency_penalty", canonical.FidelityUnsupported, "Anthropic has no frequency_penalty parameter")
	}
	if req.Seed != nil {
		d.Note("seed", canonical.FidelityUnsupported, "Anthropic has no seed parameter")
	}
	if req.N != nil && *req.N > 1 {
		d.Note("n", canonical.FidelityUnsupported, "Anthropic returns a single completion; n=%d was ignored", *req.N)
	}

	if req.Reasoning != nil && req.Reasoning.Enabled {
		budget := 0
		switch {
		case req.Reasoning.BudgetTokens != nil:
			budget = *req.Reasoning.BudgetTokens
		case req.Reasoning.Effort != "":
			budget = budgetForEffort(req.Reasoning.Effort)
			d.Note("reasoning.effort", canonical.FidelityLossy,
				"reasoning_effort=%q was approximated as a thinking budget of %d tokens", req.Reasoning.Effort, budget)
		default:
			budget = budgetForEffort(canonical.EffortMedium)
		}
		if budget < minThinkingBudget {
			budget = minThinkingBudget
		}
		// Anthropic requires max_tokens to exceed the thinking budget.
		if out.MaxTokens <= budget {
			if req.PolicyMaxTokens != nil {
				if *req.PolicyMaxTokens <= budget {
					return nil, &canonical.Error{Type: canonical.ErrInvalidRequest,
						Code: "max_output_tokens_exceeded", Param: "thinking.budget_tokens",
						Message: fmt.Sprintf("thinking budget of %d tokens cannot fit within this API key's output limit of %d", budget, *req.PolicyMaxTokens)}
				}
				out.MaxTokens = *req.PolicyMaxTokens
			} else {
				out.MaxTokens = budget + defaultMaxTokens
			}
			d.Note("max_tokens", canonical.FidelitySemantic,
				"max_tokens was raised to %d so it exceeds the thinking budget of %d, as Anthropic requires",
				out.MaxTokens, budget)
		}
		out.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
	}

	if req.ResponseFormat != nil && req.ResponseFormat.Type != canonical.FormatText {
		// Anthropic has no response_format. The closest faithful mapping is a
		// system instruction, which is a genuinely weaker guarantee.
		note := "the request asked for JSON output; Anthropic has no response_format, so the requirement was added to the system prompt instead (not enforced by the API)"
		d.Note("response_format", canonical.FidelityLossy, "%s", note)
		req = withSystemSuffix(req, jsonInstruction(req.ResponseFormat))
	}

	for _, t := range req.Tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, wireTool{
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  schema,
			CacheControl: cacheControlFrom(t.Cache),
		})
	}
	if req.ToolChoice != nil {
		tc := &wireToolChoice{DisableParallelToolUse: req.ToolChoice.ParallelDisabled}
		switch req.ToolChoice.Mode {
		case canonical.ToolChoiceAuto:
			tc.Type = "auto"
		case canonical.ToolChoiceRequired:
			tc.Type = "any"
		case canonical.ToolChoiceNone:
			tc.Type = "none"
		case canonical.ToolChoiceSpecific:
			tc.Type = "tool"
			tc.Name = req.ToolChoice.Name
		}
		out.ToolChoice = tc
	}

	if len(req.System) > 0 {
		b, err := encodeSystem(req.System)
		if err != nil {
			return nil, fmt.Errorf("encode system: %w", err)
		}
		out.System = b
	}

	msgs, err := encodeMessages(req.Messages, d)
	if err != nil {
		return nil, err
	}
	out.Messages = msgs
	if len(out.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request has no messages")
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}
	b = protocol.MergeNativeTools(protocol.Anthropic, req.NativeTools, b, d)
	return protocol.Merge(protocol.Anthropic, req.Extensions, b, d), nil
}

// encodeMessages maps canonical turns onto Anthropic's stricter model: only
// user and assistant roles, and no two consecutive turns with the same role.
func encodeMessages(msgs []canonical.Message, d *canonical.Diagnostics) ([]wireMessage, error) {
	type pending struct {
		role   string
		blocks []block
	}
	var acc []pending

	push := func(role string, blocks []block) {
		if len(blocks) == 0 {
			return
		}
		if n := len(acc); n > 0 && acc[n-1].role == role {
			acc[n-1].blocks = append(acc[n-1].blocks, blocks...)
			return
		}
		acc = append(acc, pending{role: role, blocks: blocks})
	}

	for i, m := range msgs {
		var blocks []block
		for _, p := range m.Content {
			before := len(blocks)
			switch p.Type {
			case canonical.PartText:
				if p.Text != "" {
					blocks = append(blocks, block{Type: "text", Text: p.Text})
				}
			case canonical.PartImage, canonical.PartFile:
				if b := encodeMediaBlock(p, d, fmt.Sprintf("messages[%d].content", i)); b != nil {
					blocks = append(blocks, *b)
				}
			case canonical.PartReasoning:
				if m.Role != canonical.RoleAssistant {
					continue
				}
				switch {
				case p.Reasoning != nil && p.Reasoning.Redacted != "":
					blocks = append(blocks, block{Type: "redacted_thinking", Data: p.Reasoning.Redacted})
				case p.Reasoning != nil && p.Reasoning.Signature != "":
					blocks = append(blocks, block{Type: "thinking", Thinking: p.Text, Signature: p.Reasoning.Signature})
				default:
					// Anthropic rejects a thinking block without a valid
					// signature, so reasoning that came from another protocol
					// cannot be replayed.
					d.Note(fmt.Sprintf("messages[%d].reasoning", i), canonical.FidelityLossy,
						"assistant reasoning was omitted: Anthropic only accepts thinking blocks carrying its own signature")
				}
			case canonical.PartToolCall:
				if p.ToolCall == nil {
					continue
				}
				input := p.ToolCall.Arguments
				if len(input) == 0 || !json.Valid(input) {
					// Anthropic requires a JSON object here; a truncated or
					// malformed argument string would be rejected outright.
					if len(input) > 0 {
						d.Note(fmt.Sprintf("messages[%d].tool_call", i), canonical.FidelityLossy,
							"tool call %q had unparseable arguments; an empty object was sent", p.ToolCall.Name)
					}
					input = json.RawMessage("{}")
				}
				if p.ToolCall.Signature != "" {
					// Anthropic rejects unknown keys on a tool_use block, and a
					// Gemini signature means nothing to it. Reach Gemini through
					// the gemini protocol to keep it.
					d.Note(fmt.Sprintf("messages[%d].tool_call.signature", i), canonical.FidelityLossy,
						"the thought signature on tool call %q was not sent upstream: it is meaningful "+
							"only to Gemini, which Polyglot reaches through the gemini protocol", p.ToolCall.Name)
				}
				blocks = append(blocks, block{
					Type: "tool_use", ID: p.ToolCall.ID, Name: p.ToolCall.Name, Input: input,
				})
			case canonical.PartToolResult:
				if p.ToolResult == nil {
					continue
				}
				content, err := json.Marshal(joinText(p.ToolResult.Content))
				if err != nil {
					return nil, fmt.Errorf("encode tool result: %w", err)
				}
				blocks = append(blocks, block{
					Type:      "tool_result",
					ToolUseID: p.ToolResult.ToolCallID,
					Content:   content,
					IsError:   p.ToolResult.IsError,
				})
			case canonical.PartNative:
				if p.Native == nil {
					continue
				}
				if p.Native.Protocol != string(protocol.Anthropic) {
					d.Note(fmt.Sprintf("messages[%d].native", i), canonical.FidelityUnsupported,
						"native %s block %q cannot be expressed in Anthropic", p.Native.Protocol, p.Native.Type)
					continue
				}
				blocks = append(blocks, block{Type: p.Native.Type, Raw: append(json.RawMessage(nil), p.Native.Raw...)})
			}
			markCache(blocks, before, p.Cache)
		}
		if len(blocks) == 0 {
			continue
		}

		switch m.Role {
		case canonical.RoleAssistant:
			push("assistant", blocks)
		case canonical.RoleTool:
			// Tool results belong to the following user turn in Anthropic's
			// model, so they merge with any adjacent user content.
			push("user", blocks)
		case canonical.RoleSystem:
			d.Note(fmt.Sprintf("messages[%d]", i), canonical.FidelitySemantic,
				"a system message inside the conversation was sent as a user turn")
			push("user", blocks)
		default:
			push("user", blocks)
		}
	}

	out := make([]wireMessage, 0, len(acc))
	for _, p := range acc {
		b, err := json.Marshal(p.blocks)
		if err != nil {
			return nil, fmt.Errorf("encode message content: %w", err)
		}
		out = append(out, wireMessage{Role: p.role, Content: b})
	}
	// Anthropic requires the conversation to open with a user turn.
	if len(out) > 0 && out[0].Role != "user" {
		d.Note("messages", canonical.FidelitySemantic,
			"the conversation started with an assistant turn; a placeholder user turn was prepended, as Anthropic requires")
		lead, _ := json.Marshal([]block{{Type: "text", Text: "(continue)"}})
		out = append([]wireMessage{{Role: "user", Content: lead}}, out...)
	}
	return out, nil
}

// --- response -------------------------------------------------------------

func (Codec) DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error) {
	d = d.WithStage("decode:anthropic")

	var in messagesResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned invalid JSON: %v", err)
	}
	if in.Type == "error" {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned an error payload")
	}

	resp := &canonical.Response{
		ID:           in.ID,
		Model:        in.Model,
		Created:      time.Now(),
		FinishReason: stopToCanonical(in.StopReason),
		Extensions:   protocol.Capture(protocol.Anthropic, body, protocol.Top(messagesResponse{})),
	}
	raw, err := json.Marshal(in.Content)
	if err != nil {
		return nil, fmt.Errorf("re-encode content: %w", err)
	}
	parts, err := decodeBlocks(raw, d, "content")
	if err != nil {
		return nil, err
	}
	resp.Message = canonical.Message{Role: canonical.RoleAssistant, Content: parts}
	if in.Usage != nil {
		resp.Usage = usageToCanonical(in.Usage)
	}
	return resp, nil
}

func (Codec) EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	model := resp.Model
	if model == "" && req != nil {
		model = req.Model
	}
	out := messagesResponse{
		ID:         orDefault(resp.ID, "msg_"+idgen.New()),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: stopFromCanonical(resp.FinishReason),
		Usage:      usageFromCanonical(resp.Usage),
	}
	out.Content = []block{}
	for _, p := range resp.Message.Content {
		switch p.Type {
		case canonical.PartText:
			if p.Text != "" {
				out.Content = append(out.Content, block{Type: "text", Text: p.Text})
			}
		case canonical.PartReasoning:
			b := block{Type: "thinking", Thinking: p.Text}
			if p.Reasoning != nil {
				if p.Reasoning.Redacted != "" {
					out.Content = append(out.Content, block{Type: "redacted_thinking", Data: p.Reasoning.Redacted})
					continue
				}
				b.Signature = p.Reasoning.Signature
			}
			out.Content = append(out.Content, b)
		case canonical.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			input := p.ToolCall.Arguments
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage("{}")
			}
			out.Content = append(out.Content, block{
				Type: "tool_use", ID: orDefault(p.ToolCall.ID, "toolu_"+idgen.New()),
				Name: p.ToolCall.Name, Input: input,
				// The client has to hand this back next turn for a Gemini
				// upstream to accept the call again.
				ExtraContent: protocol.SignatureExtra(p.ToolCall.Signature),
			})
		case canonical.PartNative:
			if p.Native == nil {
				continue
			}
			if p.Native.Protocol != string(protocol.Anthropic) {
				d.Note("content.native", canonical.FidelityUnsupported,
					"native %s block %q cannot be expressed in Anthropic", p.Native.Protocol, p.Native.Type)
				continue
			}
			out.Content = append(out.Content, block{Type: p.Native.Type, Raw: append(json.RawMessage(nil), p.Native.Raw...)})
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic response: %w", err)
	}
	return protocol.Merge(protocol.Anthropic, resp.Extensions, b, d.WithStage("encode:anthropic")), nil
}

func (Codec) EncodeError(err *canonical.Error) []byte {
	var out wireError
	out.Type = "error"
	out.Error.Type = errorTypeString(err.Type)
	out.Error.Message = err.Message
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return []byte(`{"type":"error","error":{"type":"api_error","message":"internal error"}}`)
	}
	return b
}

func errorTypeString(t canonical.ErrorType) string {
	switch t {
	case canonical.ErrInvalidRequest, canonical.ErrUnsupported:
		return "invalid_request_error"
	case canonical.ErrAuthentication:
		return "authentication_error"
	case canonical.ErrPermission:
		return "permission_error"
	case canonical.ErrNotFound:
		return "not_found_error"
	case canonical.ErrRateLimit:
		return "rate_limit_error"
	case canonical.ErrOverloaded:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// --- helpers --------------------------------------------------------------

// usageToCanonical sums Anthropic's three input counters into one prompt
// total. Anthropic's input_tokens excludes both cache portions and bills them
// at different rates, so the tokens the model actually read are the sum — see
// canonical.Usage for why the total is the canonical form.
func usageToCanonical(u *wireUsage) canonical.Usage {
	return canonical.Usage{
		InputTokens: u.InputTokens +
			u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  u.CacheCreationInputTokens,
	}
}

// usageFromCanonical splits the prompt total back into Anthropic's three
// counters, so a reply that arrived from any protocol adds up the way an
// Anthropic client expects to be billed.
func usageFromCanonical(u canonical.Usage) *wireUsage {
	return &wireUsage{
		InputTokens:              u.UncachedInputTokens(),
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CachedInputTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
	}
}

func stopToCanonical(s string) canonical.FinishReason {
	switch s {
	case "end_turn", "stop_sequence", "pause_turn":
		return canonical.FinishStop
	case "max_tokens":
		return canonical.FinishLength
	case "tool_use":
		return canonical.FinishToolCalls
	case "refusal":
		return canonical.FinishContentFilter
	default:
		return canonical.FinishUnknown
	}
}

func stopFromCanonical(f canonical.FinishReason) string {
	switch f {
	case canonical.FinishLength:
		return "max_tokens"
	case canonical.FinishToolCalls:
		return "tool_use"
	case canonical.FinishContentFilter:
		return "refusal"
	default:
		return "end_turn"
	}
}

// budgetForEffort is the inverse of the OpenAI codec's effortForBudget. The
// two together make reasoning settings survive a round trip approximately.
func budgetForEffort(e canonical.ReasoningEffort) int {
	switch e {
	case canonical.EffortMinimal, canonical.EffortLow:
		return 2048
	case canonical.EffortHigh:
		return 16384
	default:
		return 8192
	}
}

func jsonInstruction(rf *canonical.ResponseFormat) string {
	if rf.Type == canonical.FormatJSONSchema && len(rf.Schema) > 0 {
		return "You must reply with a single JSON value that validates against this JSON Schema, and nothing else:\n" + string(rf.Schema)
	}
	return "You must reply with a single valid JSON object and nothing else."
}

// withSystemSuffix returns a shallow copy with an extra system block, leaving
// the caller's request untouched.
func withSystemSuffix(req *canonical.Request, text string) *canonical.Request {
	cp := *req
	cp.System = append(append([]canonical.ContentPart(nil), req.System...), canonical.Text(text))
	return &cp
}

// encodeSystem writes the system prompt. A plain string is the ordinary form
// and stays the default; a breakpoint can only be expressed on a block, so a
// system prompt carrying one is written as the block array instead. Splitting
// it also preserves where the prefix ends, which is the entire meaning of the
// marker.
func encodeSystem(parts []canonical.ContentPart) (json.RawMessage, error) {
	hinted := false
	for _, p := range parts {
		if p.Cache != nil {
			hinted = true
			break
		}
	}
	if !hinted {
		return json.Marshal(joinText(parts))
	}
	blocks := []block{}
	for _, p := range parts {
		if p.Type != canonical.PartText || p.Text == "" {
			continue
		}
		blocks = append(blocks, block{
			Type: "text", Text: p.Text, CacheControl: cacheControlFrom(p.Cache),
		})
	}
	if len(blocks) == 0 {
		return json.Marshal(joinText(parts))
	}
	return json.Marshal(blocks)
}

// markCache puts a breakpoint back on the block a canonical part became.
func markCache(blocks []block, before int, h *canonical.CacheHint) {
	if h == nil || len(blocks) == before {
		return
	}
	blocks[len(blocks)-1].CacheControl = cacheControlFrom(h)
}

func joinText(parts []canonical.ContentPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == canonical.PartText {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
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

// sourceMedia reads Anthropic's attachment union into canonical form.
func sourceMedia(s *blockSource) *canonical.Media {
	if s == nil {
		return nil
	}
	switch s.Type {
	case "base64":
		if s.Data == "" {
			return nil
		}
		return &canonical.Media{MIMEType: s.MediaType, Data: s.Data}
	case "url":
		if s.URL == "" {
			return nil
		}
		return &canonical.Media{MIMEType: s.MediaType, URL: s.URL}
	case "file":
		if s.FileID == "" {
			return nil
		}
		return &canonical.Media{
			MIMEType: s.MediaType,
			FileID:   s.FileID,
			Provider: string(protocol.Anthropic),
		}
	}
	// "text" documents carry plain text rather than bytes; there is no
	// canonical attachment for that, and turning it into a text part would
	// change how the model is told to treat it.
	return nil
}

// encodeMediaBlock renders an attachment as an Anthropic image or document
// block. A file handle from another provider is reported rather than sent:
// Anthropic cannot resolve an id it did not issue.
func encodeMediaBlock(p canonical.ContentPart, d *canonical.Diagnostics, field string) *block {
	m := p.Media
	if m == nil {
		return nil
	}
	if m.Bound() && !protocol.BoundMediaUsable(protocol.Anthropic, m) {
		protocol.MediaNote(d, field, m,
			"it is a file handle issued by "+m.Provider+", which Anthropic cannot resolve")
		return nil
	}

	out := &block{Type: "document", Title: m.Filename}
	if p.Type == canonical.PartImage {
		out = &block{Type: "image"}
	}
	switch {
	case m.Inline():
		out.Source = &blockSource{Type: "base64", MediaType: m.MIMEType, Data: m.Data}
	case m.Remote():
		out.Source = &blockSource{Type: "url", URL: m.URL}
	case m.Bound():
		out.Source = &blockSource{Type: "file", FileID: m.FileID}
	default:
		protocol.MediaNote(d, field, m, "it carried no data, url or file id")
		return nil
	}
	if m.Detail != "" {
		d.Note(field, canonical.FidelityLossy,
			"the image detail hint %q was dropped: Anthropic has no equivalent", m.Detail)
	}
	return out
}
