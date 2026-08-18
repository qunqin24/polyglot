// Package responses implements OpenAI's Responses API as a Polyglot codec:
// Responses <-> Canonical, in both directions.
package responses

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

func (Codec) Name() protocol.Name { return protocol.OpenAIResponses }

// --- request: Responses -> canonical --------------------------------------

func (Codec) DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error) {
	d = d.WithStage("decode:openai-responses")

	var in responsesRequest
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
		Extensions:         protocol.Capture(protocol.OpenAIResponses, body, protocol.Top(responsesRequest{})),
		Model:              in.Model,
		Temperature:        in.Temperature,
		TopP:               in.TopP,
		MaxTokens:          in.MaxOutputTokens,
		Stream:             in.Stream,
		User:               in.User,
		Metadata:           in.Metadata,
		PreviousResponseID: in.PreviousResponseID,
		Store:              in.Store,
		// The Responses API always reports usage.
		IncludeUsage: true,
	}
	if in.Instructions != "" {
		req.System = []canonical.ContentPart{canonical.Text(in.Instructions)}
	}
	if in.Reasoning != nil {
		rc := &canonical.ReasoningConfig{Enabled: true}
		if in.Reasoning.Effort != "" {
			rc.Effort = canonical.ReasoningEffort(in.Reasoning.Effort)
		}
		// A summary setting is the client asking to see the reasoning.
		rc.Visible = in.Reasoning.Summary != ""
		req.Reasoning = rc
	}
	if rf, err := decodeFormat(in.Text); err != nil {
		return nil, err
	} else {
		req.ResponseFormat = rf
	}

	rawTools := protocol.RawArray(body, "tools")
	for i, t := range in.Tools {
		if t.Type != "function" {
			// web_search, file_search, computer_use and friends run inside
			// OpenAI. No other protocol can honour them; the Responses API
			// itself can, so they are kept for that route.
			if i < len(rawTools) {
				req.NativeTools = req.NativeTools.Add(string(protocol.OpenAIResponses), t.Type, rawTools[i])
			}
			continue
		}
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Strict:      t.Strict,
		})
	}
	tc, err := decodeToolChoice(in.ToolChoice)
	if err != nil {
		return nil, err
	}
	if in.ParallelToolCalls != nil && !*in.ParallelToolCalls {
		if tc == nil {
			tc = &canonical.ToolChoice{Mode: canonical.ToolChoiceAuto}
		}
		tc.ParallelDisabled = true
	}
	req.ToolChoice = tc

	msgs, err := decodeInput(in.Input, d)
	if err != nil {
		return nil, err
	}
	req.Messages = msgs
	if len(req.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "'input' contains no usable items")
	}
	return req, nil
}

// decodeInput handles both shapes of `input`: a bare string, or an array of
// typed items.
func decodeInput(raw json.RawMessage, d *canonical.Diagnostics) ([]canonical.Message, error) {
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'input': %v", err)
		}
		if s == "" {
			return nil, nil
		}
		return []canonical.Message{{
			Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text(s)},
		}}, nil
	}

	var items []item
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'input': %v", err)
	}

	var out []canonical.Message
	for i, it := range items {
		where := fmt.Sprintf("input[%d]", i)
		switch it.Type {
		case "function_call":
			args := json.RawMessage(it.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			out = append(out, canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{{
					Type: canonical.PartToolCall,
					ToolCall: &canonical.ToolCall{
						ID: it.CallID, Name: it.Name, Arguments: args,
						Signature: it.ExtraContent.Signature(),
					},
				}},
			})

		case "function_call_output":
			out = append(out, canonical.Message{
				Role: canonical.RoleTool,
				Content: []canonical.ContentPart{{
					Type: canonical.PartToolResult,
					ToolResult: &canonical.ToolResult{
						ToolCallID: it.CallID,
						Content:    []canonical.ContentPart{canonical.Text(it.Output)},
					},
				}},
			})

		case "reasoning":
			var sb strings.Builder
			for _, s := range it.Summary {
				sb.WriteString(s.Text)
			}
			part := canonical.ContentPart{Type: canonical.PartReasoning, Text: sb.String()}
			meta := canonical.ReasoningMeta{Provider: string(protocol.OpenAIResponses), ID: it.ID, Redacted: it.EncryptedContent}
			if meta != (canonical.ReasoningMeta{}) {
				part.Reasoning = &meta
			}
			out = append(out, canonical.Message{
				Role: canonical.RoleAssistant, Content: []canonical.ContentPart{part},
			})

		case "message", "":
			role := canonical.RoleUser
			switch it.Role {
			case "assistant":
				role = canonical.RoleAssistant
			case "system", "developer":
				role = canonical.RoleSystem
			}
			parts, err := decodeContent(it.Content, d, where)
			if err != nil {
				return nil, err
			}
			if len(parts) > 0 {
				out = append(out, canonical.Message{Role: role, Content: parts})
			}

		default:
			out = append(out, canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
				Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(protocol.OpenAIResponses), Type: it.Type,
					Raw: append(json.RawMessage(nil), it.Raw...)},
			}}})
		}
	}
	return out, nil
}

func decodeContent(raw json.RawMessage, d *canonical.Diagnostics, where string) ([]canonical.ContentPart, error) {
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
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "%s: invalid content: %v", where, err)
	}
	var out []canonical.ContentPart
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text", "":
			if p.Text != "" {
				out = append(out, canonical.Text(p.Text))
			}
		case "refusal":
			if p.Text != "" {
				out = append(out, canonical.Text(p.Text))
			}
		case "input_image":
			m := imageMedia(p)
			if m == nil {
				d.Note(where, canonical.FidelityUnsupported, "an input_image carried no url or file id")
				continue
			}
			out = append(out, canonical.ContentPart{Type: canonical.PartImage, Media: m})
		case "input_file":
			m := inputFileMedia(p)
			if m == nil {
				d.Note(where, canonical.FidelityUnsupported, "an input_file carried no data or file id")
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
			d.Note(where, canonical.FidelityUnsupported,
				"content part of type %q was dropped: Polyglot does not convert it yet", p.Type)
		}
	}
	return out, nil
}

func decodeFormat(t *textConfig) (*canonical.ResponseFormat, error) {
	if t == nil || t.Format == nil {
		return nil, nil
	}
	switch t.Format.Type {
	case "text":
		return &canonical.ResponseFormat{Type: canonical.FormatText}, nil
	case "json_object":
		return &canonical.ResponseFormat{Type: canonical.FormatJSONObject}, nil
	case "json_schema":
		return &canonical.ResponseFormat{
			Type:   canonical.FormatJSONSchema,
			Name:   t.Format.Name,
			Schema: t.Format.Schema,
			Strict: t.Format.Strict,
		}, nil
	default:
		return nil, canonical.Errorf(canonical.ErrInvalidRequest,
			"unsupported text.format.type %q", t.Format.Type)
	}
}

func decodeToolChoice(raw json.RawMessage) (*canonical.ToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'tool_choice': %v", err)
		}
		switch s {
		case "auto":
			return &canonical.ToolChoice{Mode: canonical.ToolChoiceAuto}, nil
		case "none":
			return &canonical.ToolChoice{Mode: canonical.ToolChoiceNone}, nil
		case "required":
			return &canonical.ToolChoice{Mode: canonical.ToolChoiceRequired}, nil
		default:
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "unsupported tool_choice %q", s)
		}
	}
	// Responses names the function directly rather than nesting it.
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'tool_choice': %v", err)
	}
	if obj.Type != "function" || obj.Name == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest,
			"tool_choice must name a function, e.g. {\"type\":\"function\",\"name\":\"f\"}")
	}
	return &canonical.ToolChoice{Mode: canonical.ToolChoiceSpecific, Name: obj.Name}, nil
}

// --- request: canonical -> Responses --------------------------------------

func (Codec) EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:openai-responses")
	protocol.NoteCacheHints(req, d)
	protocol.NoteTextSignatures(req, d)

	out := responsesRequest{
		Model:           req.Model,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		Stream:          req.Stream,
		User:            req.User,
		Metadata:        req.Metadata,
		// Preserve the caller's state settings on a Responses route. When the
		// caller omitted store, explicitly disable it: the API defaults to
		// retaining responses, while a stateless gateway should not opt clients
		// into storage implicitly.
		Store:              req.Store,
		PreviousResponseID: req.PreviousResponseID,
	}
	if out.Store == nil {
		out.Store = canonical.Ptr(false)
	}
	if len(req.System) > 0 {
		out.Instructions = joinText(req.System)
	}

	// Parameters the Responses API does not expose. Recorded rather than
	// dropped, and not sent: an unknown field would be rejected outright.
	if req.TopK != nil {
		d.Note("top_k", canonical.FidelityUnsupported, "the Responses API has no top_k parameter")
	}
	if req.PresencePenalty != nil {
		d.Note("presence_penalty", canonical.FidelityUnsupported, "the Responses API has no presence_penalty parameter")
	}
	if req.FrequencyPenalty != nil {
		d.Note("frequency_penalty", canonical.FidelityUnsupported, "the Responses API has no frequency_penalty parameter")
	}
	if req.Seed != nil {
		d.Note("seed", canonical.FidelityUnsupported, "the Responses API has no seed parameter")
	}
	if len(req.Stop) > 0 {
		d.Note("stop", canonical.FidelityUnsupported,
			"the Responses API has no stop sequence parameter; %d stop sequence(s) were dropped", len(req.Stop))
	}
	if req.N != nil && *req.N > 1 {
		d.Note("n", canonical.FidelityUnsupported,
			"the Responses API returns a single response; n=%d was ignored", *req.N)
	}

	if req.Reasoning != nil && req.Reasoning.Enabled {
		rc := &reasoningConfig{Effort: string(req.Reasoning.Effort)}
		if rc.Effort == "" && req.Reasoning.BudgetTokens != nil {
			rc.Effort = effortForBudget(*req.Reasoning.BudgetTokens)
			d.Note("reasoning.budget_tokens", canonical.FidelityLossy,
				"a thinking budget of %d tokens was approximated as reasoning.effort=%q",
				*req.Reasoning.BudgetTokens, rc.Effort)
		}
		if req.Reasoning.Visible {
			rc.Summary = "auto"
		}
		out.Reasoning = rc
	}

	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case canonical.FormatText:
			out.Text = &textConfig{Format: &formatSpec{Type: "text"}}
		case canonical.FormatJSONObject:
			out.Text = &textConfig{Format: &formatSpec{Type: "json_object"}}
		case canonical.FormatJSONSchema:
			out.Text = &textConfig{Format: &formatSpec{
				Type:   "json_schema",
				Name:   orDefault(req.ResponseFormat.Name, "response"),
				Schema: req.ResponseFormat.Schema,
				Strict: req.ResponseFormat.Strict,
			}}
		}
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, wireTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Strict:      t.Strict,
		})
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case canonical.ToolChoiceAuto:
			out.ToolChoice = json.RawMessage(`"auto"`)
		case canonical.ToolChoiceNone:
			out.ToolChoice = json.RawMessage(`"none"`)
		case canonical.ToolChoiceRequired:
			out.ToolChoice = json.RawMessage(`"required"`)
		case canonical.ToolChoiceSpecific:
			b, err := json.Marshal(map[string]string{"type": "function", "name": req.ToolChoice.Name})
			if err != nil {
				return nil, fmt.Errorf("encode tool_choice: %w", err)
			}
			out.ToolChoice = b
		}
		if req.ToolChoice.ParallelDisabled {
			out.ParallelToolCalls = canonical.Ptr(false)
		}
	}

	items, err := encodeInput(req.Messages, d)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request has no messages")
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode input: %w", err)
	}
	out.Input = raw

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode responses request: %w", err)
	}
	b = protocol.MergeNativeTools(protocol.OpenAIResponses, req.NativeTools, b, d)
	return protocol.Merge(protocol.OpenAIResponses, req.Extensions, b, d), nil
}

func encodeInput(msgs []canonical.Message, d *canonical.Diagnostics) ([]item, error) {
	var out []item

	for i, m := range msgs {
		var text strings.Builder
		var media []contentPart
		for _, p := range m.Content {
			switch p.Type {
			case canonical.PartText:
				text.WriteString(p.Text)

			case canonical.PartImage, canonical.PartFile:
				if cp := encodeMediaPart(p, d, fmt.Sprintf("messages[%d].content", i)); cp != nil {
					media = append(media, *cp)
				}

			case canonical.PartReasoning:
				// A reasoning item can only be replayed with the identifiers
				// OpenAI issued for it; reasoning from another provider has
				// none, so it cannot be sent back.
				if m.Role != canonical.RoleAssistant {
					continue
				}
				if p.Reasoning != nil && p.Reasoning.Provider == string(protocol.OpenAIResponses) &&
					(p.Reasoning.ID != "" || p.Reasoning.Redacted != "") {
					it := item{Type: "reasoning", ID: p.Reasoning.ID, EncryptedContent: p.Reasoning.Redacted}
					if p.Text != "" {
						it.Summary = []summaryPart{{Type: "summary_text", Text: p.Text}}
					}
					out = append(out, it)
					continue
				}
				d.Note(fmt.Sprintf("messages[%d].reasoning", i), canonical.FidelityLossy,
					"assistant reasoning was omitted: the Responses API only accepts reasoning items it issued itself")

			case canonical.PartNative:
				if p.Native == nil {
					continue
				}
				if p.Native.Protocol != string(protocol.OpenAIResponses) {
					d.Note(fmt.Sprintf("messages[%d].native", i), canonical.FidelityUnsupported,
						"native %s item %q cannot be expressed in the Responses API", p.Native.Protocol, p.Native.Type)
					continue
				}
				out = append(out, item{Type: p.Native.Type, Raw: append(json.RawMessage(nil), p.Native.Raw...)})

			case canonical.PartToolCall:
				if p.ToolCall == nil {
					continue
				}
				args := p.ToolCall.Arguments
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				if p.ToolCall.Signature != "" {
					// extra_content is Google's extension, not OpenAI's. Sending
					// it to an upstream that never defined it risks a rejected
					// request. Reach Gemini through the gemini protocol to keep it.
					d.Note(fmt.Sprintf("messages[%d].tool_call.signature", i), canonical.FidelityLossy,
						"the thought signature on tool call %q was not sent upstream: it is meaningful "+
							"only to Gemini, which Polyglot reaches through the gemini protocol", p.ToolCall.Name)
				}
				out = append(out, item{
					Type:      "function_call",
					CallID:    orDefault(p.ToolCall.ID, "call_"+idgen.New()),
					Name:      p.ToolCall.Name,
					Arguments: string(args),
				})

			case canonical.PartToolResult:
				if p.ToolResult == nil {
					continue
				}
				out = append(out, item{
					Type:   "function_call_output",
					CallID: p.ToolResult.ToolCallID,
					Output: joinText(p.ToolResult.Content),
				})
			}
		}

		if s := text.String(); s != "" || len(media) > 0 {
			role, partType := "user", "input_text"
			switch m.Role {
			case canonical.RoleAssistant:
				role, partType = "assistant", "output_text"
			case canonical.RoleSystem:
				role = "system"
			}
			parts := make([]contentPart, 0, len(media)+1)
			if s != "" {
				parts = append(parts, contentPart{Type: partType, Text: s})
			}
			parts = append(parts, media...)
			content, err := json.Marshal(parts)
			if err != nil {
				return nil, fmt.Errorf("encode content: %w", err)
			}
			out = append(out, item{Type: "message", Role: role, Content: content})
		}
	}
	return out, nil
}

// --- response -------------------------------------------------------------

func (Codec) DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error) {
	d = d.WithStage("decode:openai-responses")

	var in responsesResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned invalid JSON: %v", err)
	}
	if in.Error != nil && in.Error.Message != "" {
		return nil, canonical.Errorf(canonical.ErrUpstream, "%s", in.Error.Message)
	}
	// Every Responses reply carries an output array. Its absence means the
	// upstream is not speaking this protocol, which is worth saying plainly
	// rather than handing back an empty message.
	if in.Output == nil {
		return nil, canonical.Errorf(canonical.ErrUpstream,
			"upstream reply has no 'output' array; is this provider really speaking the Responses API?")
	}

	resp := &canonical.Response{
		ID:         in.ID,
		Model:      in.Model,
		Created:    time.Unix(in.CreatedAt, 0),
		Extensions: protocol.Capture(protocol.OpenAIResponses, body, protocol.Top(responsesResponse{})),
	}
	if in.CreatedAt == 0 {
		resp.Created = time.Now()
	}

	msg := canonical.Message{Role: canonical.RoleAssistant}
	hasToolCall := false
	for _, it := range in.Output {
		switch it.Type {
		case "reasoning":
			var sb strings.Builder
			for _, s := range it.Summary {
				sb.WriteString(s.Text)
			}
			part := canonical.ContentPart{Type: canonical.PartReasoning, Text: sb.String()}
			meta := canonical.ReasoningMeta{Provider: string(protocol.OpenAIResponses), ID: it.ID, Redacted: it.EncryptedContent}
			if meta != (canonical.ReasoningMeta{}) {
				part.Reasoning = &meta
			}
			msg.Content = append(msg.Content, part)

		case "function_call":
			hasToolCall = true
			args := json.RawMessage(it.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID: orDefault(it.CallID, it.ID), Name: it.Name, Arguments: args,
					Signature: it.ExtraContent.Signature(),
				},
			})

		case "message":
			parts, err := decodeContent(it.Content, d, "output")
			if err != nil {
				return nil, err
			}
			msg.Content = append(msg.Content, parts...)

		default:
			msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(protocol.OpenAIResponses), Type: it.Type,
					Raw: append(json.RawMessage(nil), it.Raw...)}})
		}
	}
	resp.Message = msg
	resp.FinishReason = finishFor(in, hasToolCall)

	if in.Usage != nil {
		resp.Usage = usageToCanonical(in.Usage)
	}
	return resp, nil
}

// finishFor derives a finish reason. The Responses API reports completion via
// status plus incomplete_details rather than a single field, and a tool call
// is signalled only by a function_call item being present.
func finishFor(in responsesResponse, hasToolCall bool) canonical.FinishReason {
	if in.Status == "incomplete" && in.IncompleteDetails != nil {
		switch in.IncompleteDetails.Reason {
		case "max_output_tokens":
			return canonical.FinishLength
		case "content_filter":
			return canonical.FinishContentFilter
		}
	}
	if in.Status == "failed" {
		return canonical.FinishError
	}
	if hasToolCall {
		return canonical.FinishToolCalls
	}
	return canonical.FinishStop
}

func (Codec) EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	protocol.NoteCacheWrite(resp.Usage, d.WithStage("encode:openai-responses"))
	model := resp.Model
	if model == "" && req != nil {
		model = req.Model
	}
	created := resp.Created
	if created.IsZero() {
		created = time.Now()
	}

	out := responsesResponse{
		ID:        orDefault(resp.ID, "resp_"+idgen.New()),
		Object:    "response",
		CreatedAt: created.Unix(),
		Model:     model,
		Status:    "completed",
		Output:    []item{},
		Usage: &wireUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
	if resp.Usage.ReasoningTokens > 0 {
		out.Usage.OutputTokensDetails = &struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{ReasoningTokens: resp.Usage.ReasoningTokens}
	}
	if resp.Usage.CachedInputTokens > 0 {
		out.Usage.InputTokensDetails = &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: resp.Usage.CachedInputTokens}
	}
	if resp.FinishReason == canonical.FinishLength {
		out.Status = "incomplete"
		out.IncompleteDetails = &struct {
			Reason string `json:"reason"`
		}{Reason: "max_output_tokens"}
	}

	var text strings.Builder
	for _, p := range resp.Message.Content {
		switch p.Type {
		case canonical.PartReasoning:
			it := item{Type: "reasoning", ID: "rs_" + idgen.New()}
			if p.Reasoning != nil {
				if p.Reasoning.ID != "" {
					it.ID = p.Reasoning.ID
				}
				it.EncryptedContent = p.Reasoning.Redacted
			}
			if p.Text != "" {
				it.Summary = []summaryPart{{Type: "summary_text", Text: p.Text}}
			}
			out.Output = append(out.Output, it)
		case canonical.PartText:
			text.WriteString(p.Text)
		case canonical.PartNative:
			if p.Native == nil {
				continue
			}
			if p.Native.Protocol != string(protocol.OpenAIResponses) {
				d.Note("output.native", canonical.FidelityUnsupported,
					"native %s item %q cannot be expressed in the Responses API", p.Native.Protocol, p.Native.Type)
				continue
			}
			out.Output = append(out.Output, item{Type: p.Native.Type, Raw: append(json.RawMessage(nil), p.Native.Raw...)})
		}
	}
	if s := text.String(); s != "" {
		content, err := json.Marshal([]contentPart{{Type: "output_text", Text: s}})
		if err != nil {
			return nil, fmt.Errorf("encode output text: %w", err)
		}
		out.Output = append(out.Output, item{
			Type: "message", ID: "msg_" + idgen.New(), Role: "assistant",
			Status: "completed", Content: content,
		})
	}
	for _, p := range resp.Message.Content {
		if p.Type != canonical.PartToolCall || p.ToolCall == nil {
			continue
		}
		args := p.ToolCall.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		out.Output = append(out.Output, item{
			Type:      "function_call",
			ID:        "fc_" + idgen.New(),
			CallID:    orDefault(p.ToolCall.ID, "call_"+idgen.New()),
			Name:      p.ToolCall.Name,
			Arguments: string(args),
			Status:    "completed",
			// The client has to hand this back next turn for a Gemini upstream
			// to accept the call again.
			ExtraContent: protocol.SignatureExtra(p.ToolCall.Signature),
		})
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode responses response: %w", err)
	}
	return protocol.Merge(protocol.OpenAIResponses, resp.Extensions, b, d.WithStage("encode:openai-responses")), nil
}

func (Codec) EncodeError(err *canonical.Error) []byte {
	out := wireError{Error: wireErrorDetail{
		Message: err.Message,
		Type:    errorTypeString(err.Type),
		Code:    err.Code,
		Param:   err.Param,
	}}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return []byte(`{"error":{"message":"internal error","type":"api_error"}}`)
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
		return "server_error"
	default:
		return "api_error"
	}
}

// --- helpers --------------------------------------------------------------

func usageToCanonical(u *wireUsage) canonical.Usage {
	out := canonical.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	if u.InputTokensDetails != nil {
		out.CachedInputTokens = u.InputTokensDetails.CachedTokens
	}
	return out
}

// effortForBudget mirrors the OpenAI codec's mapping so a thinking budget
// converts the same way regardless of which OpenAI surface it goes to.
func effortForBudget(budget int) string {
	switch {
	case budget <= 2048:
		return "low"
	case budget <= 8192:
		return "medium"
	default:
		return "high"
	}
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

// imageMedia reads an input_image part. The Responses API spells the payload
// as a url that may be a data: URI, or as a handle for an uploaded file.
func imageMedia(p contentPart) *canonical.Media {
	switch {
	case p.ImageURL != "":
		m := protocol.MediaFromURL(p.ImageURL)
		m.Detail = p.Detail
		return m
	case p.FileID != "":
		return &canonical.Media{
			FileID:   p.FileID,
			Provider: string(protocol.OpenAIResponses),
			Detail:   p.Detail,
		}
	}
	return nil
}

// inputFileMedia reads an input_file part.
func inputFileMedia(p contentPart) *canonical.Media {
	switch {
	case p.FileData != "":
		m := protocol.MediaFromURL(p.FileData)
		m.Filename = p.Filename
		return m
	case p.FileID != "":
		return &canonical.Media{
			FileID:   p.FileID,
			Provider: string(protocol.OpenAIResponses),
			Filename: p.Filename,
		}
	}
	return nil
}

// encodeMediaPart renders an attachment as a Responses content part.
//
// A file id issued by OpenAI's own file store is usable here; one from any
// other provider is not, and saying so beats sending a handle the upstream
// will reject.
func encodeMediaPart(p canonical.ContentPart, d *canonical.Diagnostics, field string) *contentPart {
	m := p.Media
	if m == nil {
		return nil
	}
	usableID := protocol.BoundMediaUsable(protocol.OpenAIResponses, m) ||
		(m.Bound() && m.Provider == string(protocol.OpenAI))
	if m.Bound() && !usableID {
		protocol.MediaNote(d, field, m,
			"it is a file handle issued by "+m.Provider+", which the Responses API cannot resolve")
		return nil
	}

	if p.Type == canonical.PartImage {
		out := &contentPart{Type: "input_image", Detail: m.Detail}
		switch {
		case m.Inline():
			out.ImageURL = protocol.DataURI(m.MIMEType, m.Data)
		case m.Remote():
			out.ImageURL = m.URL
		case m.Bound():
			out.FileID = m.FileID
		}
		return out
	}

	out := &contentPart{Type: "input_file", Filename: m.Filename}
	switch {
	case m.Inline():
		out.FileData = protocol.DataURI(m.MIMEType, m.Data)
	case m.Bound():
		out.FileID = m.FileID
	default:
		protocol.MediaNote(d, field, m,
			"the Responses API accepts a document inline or by file id, not by url")
		return nil
	}
	return out
}
