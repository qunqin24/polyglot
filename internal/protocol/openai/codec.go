// Package openai implements the OpenAI Chat Completions protocol as a
// Polyglot codec: OpenAI <-> Canonical, in both directions.
package openai

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

type Codec struct{}

func init() { protocol.Register(Codec{}) }

func (Codec) Name() protocol.Name { return protocol.OpenAI }

// --- request: OpenAI -> canonical -----------------------------------------

func (Codec) DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error) {
	d = d.WithStage("decode:openai")

	var in chatRequest
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
		// Whatever this dialect added and Polyglot does not model — OpenRouter's
		// `provider`, vLLM's `guided_json`, DeepSeek's `prefix` — rides along
		// instead of being discarded by the unmarshal above.
		Extensions:       protocol.Capture(protocol.OpenAI, body, protocol.Top(chatRequest{})),
		Model:            in.Model,
		Temperature:      in.Temperature,
		TopP:             in.TopP,
		PresencePenalty:  in.PresencePenalty,
		FrequencyPenalty: in.FrequencyPenalty,
		Seed:             in.Seed,
		N:                in.N,
		Stream:           in.Stream,
		User:             in.User,
		Metadata:         in.Metadata,
	}
	// max_completion_tokens supersedes the deprecated max_tokens.
	if in.MaxCompletionTokens != nil {
		req.MaxTokens = in.MaxCompletionTokens
	} else if in.MaxTokens != nil {
		req.MaxTokens = in.MaxTokens
	}
	if in.StreamOptions != nil && in.StreamOptions.IncludeUsage {
		req.IncludeUsage = true
	}
	if stop, err := decodeStop(in.Stop); err != nil {
		return nil, err
	} else {
		req.Stop = stop
	}
	if in.ReasoningEffort != "" {
		req.Reasoning = &canonical.ReasoningConfig{
			Enabled: true,
			Effort:  canonical.ReasoningEffort(in.ReasoningEffort),
			Visible: true,
		}
	}
	if rf, err := decodeResponseFormat(in.ResponseFormat); err != nil {
		return nil, err
	} else {
		req.ResponseFormat = rf
	}
	rawTools := protocol.RawArray(body, "tools")
	for i, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			// A provider-executed tool. Polyglot cannot relay it, but the
			// upstream can run it, so it is kept for a same-protocol route.
			if i < len(rawTools) {
				req.NativeTools = req.NativeTools.Add(string(protocol.OpenAI), t.Type, rawTools[i])
			}
			continue
		}
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      t.Function.Strict,
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

	sawNonSystem := false
	for i, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			parts, err := decodeContent(m.Content, d)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", i, err)
			}
			if sawNonSystem {
				// Canonical models system prompts as a single top-level
				// block, matching Anthropic and Gemini. A system message in
				// the middle of a conversation is hoisted, which changes its
				// position relative to the other turns.
				d.Note(fmt.Sprintf("messages[%d]", i), canonical.FidelitySemantic,
					"system message appearing after a conversation turn was hoisted into the system prompt")
			}
			req.System = append(req.System, parts...)
		case "user", "assistant", "tool", "function":
			sawNonSystem = true
			msg, err := decodeMessage(m, d, i)
			if err != nil {
				return nil, err
			}
			req.Messages = append(req.Messages, msg)
		default:
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "messages[%d]: unknown role %q", i, m.Role)
		}
	}
	return req, nil
}

func decodeMessage(m wireMessage, d *canonical.Diagnostics, idx int) (canonical.Message, error) {
	out := canonical.Message{Name: m.Name}

	switch m.Role {
	case "tool", "function":
		out.Role = canonical.RoleTool
		parts, err := decodeContent(m.Content, d)
		if err != nil {
			return out, fmt.Errorf("messages[%d]: %w", idx, err)
		}
		out.Content = []canonical.ContentPart{{
			Type: canonical.PartToolResult,
			ToolResult: &canonical.ToolResult{
				ToolCallID: m.ToolCallID,
				Name:       m.Name,
				Content:    parts,
			},
		}}
		return out, nil
	case "assistant":
		out.Role = canonical.RoleAssistant
	default:
		out.Role = canonical.RoleUser
	}

	if r := firstNonEmpty(m.ReasoningContent, m.Reasoning); r != "" {
		out.Content = append(out.Content, canonical.ContentPart{Type: canonical.PartReasoning, Text: r,
			Reasoning: &canonical.ReasoningMeta{Provider: string(protocol.OpenAI)}})
	}
	parts, err := decodeContent(m.Content, d)
	if err != nil {
		return out, fmt.Errorf("messages[%d]: %w", idx, err)
	}
	out.Content = append(out.Content, parts...)

	if m.Refusal != nil && *m.Refusal != "" {
		d.Note(fmt.Sprintf("messages[%d].refusal", idx), canonical.FidelitySemantic,
			"assistant refusal was represented as text")
		out.Content = append(out.Content, canonical.Text(*m.Refusal))
	}
	for _, tc := range m.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		out.Content = append(out.Content, canonical.ContentPart{
			Type: canonical.PartToolCall,
			ToolCall: &canonical.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: args,
				Signature: tc.ExtraContent.Signature(),
			},
		})
	}
	return out, nil
}

// decodeContent handles OpenAI's three content shapes: null, a bare string, or
// an array of typed parts.
func decodeContent(raw json.RawMessage, d *canonical.Diagnostics) ([]canonical.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid string content: %v", err)
		}
		if s == "" {
			return nil, nil
		}
		return []canonical.ContentPart{canonical.Text(s)}, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid content array: %v", err)
	}
	var out []canonical.ContentPart
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text", "output_text", "":
			if p.Text != "" {
				out = append(out, canonical.Text(p.Text))
			}
		case "image_url", "input_image":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				d.Note("content", canonical.FidelityUnsupported, "an image part carried no url")
				continue
			}
			m := protocol.MediaFromURL(p.ImageURL.URL)
			m.Detail = p.ImageURL.Detail
			out = append(out, canonical.ContentPart{Type: canonical.PartImage, Media: m})
		case "file", "input_file":
			m := fileMedia(p.File)
			if m == nil {
				d.Note("content", canonical.FidelityUnsupported, "a file part carried neither data nor an id")
				continue
			}
			kind, ok := protocol.ClassifyMedia(m.MIMEType)
			if !ok {
				d.Note("content", canonical.FidelityUnsupported,
					"%s content is not converted yet and was not forwarded", m.MIMEType)
				continue
			}
			out = append(out, canonical.ContentPart{Type: kind, Media: m})
		case "input_audio":
			// Audio conversion is not implemented; say so rather than drop it.
			d.Note("content", canonical.FidelityUnsupported,
				"audio input is not converted yet and was not forwarded")
		default:
			d.Note("content", canonical.FidelityUnsupported, "unknown content part type %q was dropped", p.Type)
		}
	}
	return out, nil
}

func decodeStop(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'stop': %v", err)
		}
		return []string{s}, nil
	}
	var ss []string
	if err := json.Unmarshal(raw, &ss); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'stop': %v", err)
	}
	return ss, nil
}

func decodeResponseFormat(rf *responseFormat) (*canonical.ResponseFormat, error) {
	if rf == nil {
		return nil, nil
	}
	switch rf.Type {
	case "text":
		return &canonical.ResponseFormat{Type: canonical.FormatText}, nil
	case "json_object":
		return &canonical.ResponseFormat{Type: canonical.FormatJSONObject}, nil
	case "json_schema":
		if rf.JSONSchema == nil {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "response_format.json_schema is required when type is json_schema")
		}
		return &canonical.ResponseFormat{
			Type:   canonical.FormatJSONSchema,
			Name:   rf.JSONSchema.Name,
			Schema: rf.JSONSchema.Schema,
			Strict: rf.JSONSchema.Strict,
		}, nil
	default:
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "unsupported response_format.type %q", rf.Type)
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
		case "required", "any":
			return &canonical.ToolChoice{Mode: canonical.ToolChoiceRequired}, nil
		default:
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "unsupported tool_choice %q", s)
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid 'tool_choice': %v", err)
	}
	if obj.Function.Name == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "tool_choice.function.name is required")
	}
	return &canonical.ToolChoice{Mode: canonical.ToolChoiceSpecific, Name: obj.Function.Name}, nil
}

// --- request: canonical -> OpenAI -----------------------------------------

func (Codec) EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:openai")
	protocol.NoteResponseState(req, protocol.OpenAI, d)
	protocol.NoteCacheHints(req, d)
	protocol.NoteTextSignatures(req, d)

	out := chatRequest{
		Model:               req.Model,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		N:                   req.N,
		Stream:              req.Stream,
		MaxCompletionTokens: req.MaxTokens,
		PresencePenalty:     req.PresencePenalty,
		FrequencyPenalty:    req.FrequencyPenalty,
		Seed:                req.Seed,
		User:                req.User,
		Metadata:            req.Metadata,
	}
	if req.Stream && req.IncludeUsage {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.TopK != nil {
		d.Note("top_k", canonical.FidelityUnsupported, "OpenAI Chat Completions has no top_k parameter")
	}
	if len(req.Stop) > 0 {
		b, err := json.Marshal(req.Stop)
		if err != nil {
			return nil, fmt.Errorf("encode stop: %w", err)
		}
		out.Stop = b
	}
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		out.ReasoningEffort = string(req.Reasoning.Effort)
	} else if req.Reasoning != nil && req.Reasoning.BudgetTokens != nil {
		out.ReasoningEffort = effortForBudget(*req.Reasoning.BudgetTokens)
		d.Note("reasoning.budget_tokens", canonical.FidelityLossy,
			"a thinking budget of %d tokens was approximated as reasoning_effort=%q",
			*req.Reasoning.BudgetTokens, out.ReasoningEffort)
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case canonical.FormatText:
			out.ResponseFormat = &responseFormat{Type: "text"}
		case canonical.FormatJSONObject:
			out.ResponseFormat = &responseFormat{Type: "json_object"}
		case canonical.FormatJSONSchema:
			out.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchemaSpec{
				Name:   orDefault(req.ResponseFormat.Name, "response"),
				Schema: req.ResponseFormat.Schema,
				Strict: req.ResponseFormat.Strict,
			}}
		}
	}
	for _, t := range req.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		wt.Function.Strict = t.Strict
		out.Tools = append(out.Tools, wt)
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
			b, err := json.Marshal(map[string]any{
				"type":     "function",
				"function": map[string]string{"name": req.ToolChoice.Name},
			})
			if err != nil {
				return nil, fmt.Errorf("encode tool_choice: %w", err)
			}
			out.ToolChoice = b
		}
		if req.ToolChoice.ParallelDisabled {
			out.ParallelToolCalls = canonical.Ptr(false)
		}
	}

	if len(req.System) > 0 {
		out.Messages = append(out.Messages, wireMessage{
			Role:    "system",
			Content: mustJSONString(joinText(req.System)),
		})
	}
	for i, m := range req.Messages {
		msgs, err := encodeMessage(m, d, i)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, msgs...)
	}
	if len(out.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request has no messages")
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	b = protocol.MergeNativeTools(protocol.OpenAI, req.NativeTools, b, d)
	return protocol.Merge(protocol.OpenAI, req.Extensions, b, d), nil
}

// encodeMessage may produce several OpenAI messages: a canonical tool turn
// carrying two results becomes two OpenAI 'tool' messages.
func encodeMessage(m canonical.Message, d *canonical.Diagnostics, idx int) ([]wireMessage, error) {
	if m.Role == canonical.RoleTool {
		var out []wireMessage
		for _, p := range m.Content {
			if p.Type != canonical.PartToolResult || p.ToolResult == nil {
				continue
			}
			text := joinText(p.ToolResult.Content)
			if p.ToolResult.IsError && text != "" {
				text = "Error: " + text
			}
			out = append(out, wireMessage{
				Role:       "tool",
				ToolCallID: p.ToolResult.ToolCallID,
				Content:    mustJSONString(text),
			})
		}
		if len(out) == 0 {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest, "messages[%d]: tool turn has no tool results", idx)
		}
		return out, nil
	}

	wm := wireMessage{Role: string(m.Role), Name: m.Name}
	var text strings.Builder
	// media collects attachments. While there are none the content stays a
	// plain string, which is what every OpenAI-compatible provider expects and
	// what the overwhelming majority of requests are.
	var media []contentPart
	for _, p := range m.Content {
		switch p.Type {
		case canonical.PartText:
			text.WriteString(p.Text)
		case canonical.PartImage, canonical.PartFile:
			if cp := encodeMedia(p, d, fmt.Sprintf("messages[%d].content", idx)); cp != nil {
				media = append(media, *cp)
			}
		case canonical.PartReasoning:
			if m.Role == canonical.RoleAssistant && p.Reasoning != nil &&
				p.Reasoning.Provider == string(protocol.OpenAI) {
				wm.ReasoningContent += p.Text
				continue
			}
			d.Note(fmt.Sprintf("messages[%d].reasoning", idx), canonical.FidelityLossy,
				"assistant reasoning from a previous turn was omitted: OpenAI Chat Completions does not accept it as input")
		case canonical.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			tc := wireToolCall{ID: p.ToolCall.ID, Type: "function"}
			tc.Function.Name = p.ToolCall.Name
			tc.Function.Arguments = argsString(p.ToolCall.Arguments)
			if p.ToolCall.Signature != "" {
				// extra_content is Google's own extension. Sending it to an
				// upstream that never defined it risks a rejected request, and
				// no OpenAI-protocol provider can act on a Gemini signature.
				// Reach Gemini through the gemini protocol to keep it.
				d.Note(fmt.Sprintf("messages[%d].tool_call.signature", idx), canonical.FidelityLossy,
					"the thought signature on tool call %q was not sent upstream: it is meaningful "+
						"only to Gemini, which Polyglot reaches through the gemini protocol", p.ToolCall.Name)
			}
			wm.ToolCalls = append(wm.ToolCalls, tc)
		case canonical.PartToolResult:
			d.Note(fmt.Sprintf("messages[%d]", idx), canonical.FidelitySemantic,
				"tool result inside a %s message was converted to a separate tool message", m.Role)
		case canonical.PartNative:
			protocol.NoteNativeContent(p, protocol.OpenAI, fmt.Sprintf("messages[%d].content", idx), d)
		}
	}
	switch {
	case len(media) > 0:
		// With an attachment the content has to become an array of parts. Any
		// text goes in first, which is the order every provider documents.
		parts := make([]contentPart, 0, len(media)+1)
		if s := text.String(); s != "" {
			parts = append(parts, contentPart{Type: "text", Text: s})
		}
		parts = append(parts, media...)
		raw, err := json.Marshal(parts)
		if err != nil {
			return nil, fmt.Errorf("encode message content: %w", err)
		}
		wm.Content = raw
	case text.Len() > 0 || len(wm.ToolCalls) == 0:
		wm.Content = mustJSONString(text.String())
	}
	out := []wireMessage{wm}

	// A user turn may carry tool results (Anthropic models them that way).
	for _, p := range m.Content {
		if p.Type == canonical.PartToolResult && p.ToolResult != nil {
			out = append(out, wireMessage{
				Role:       "tool",
				ToolCallID: p.ToolResult.ToolCallID,
				Content:    mustJSONString(joinText(p.ToolResult.Content)),
			})
		}
	}
	if len(out) > 1 && len(wm.ToolCalls) == 0 && len(joinText(m.Content)) == 0 {
		// The original message was nothing but tool results; drop the empty
		// shell so we do not send a blank user turn.
		out = out[1:]
	}
	return out, nil
}

// --- response: OpenAI -> canonical ----------------------------------------

func (Codec) DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error) {
	d = d.WithStage("decode:openai")

	var in chatResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned invalid JSON: %v", err)
	}
	// Reply fields this dialect adds — system_fingerprint, OpenRouter's
	// provider and citations — travel back to a client speaking the same
	// protocol rather than being lost in the hub.
	ext := protocol.Capture(protocol.OpenAI, body, protocol.Top(chatResponse{}))
	if len(in.Choices) == 0 {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream response contains no choices")
	}
	if len(in.Choices) > 1 {
		d.Note("choices", canonical.FidelityLossy, "upstream returned %d choices; only the first is used", len(in.Choices))
	}
	ch := in.Choices[0]

	resp := &canonical.Response{
		ID:           in.ID,
		Model:        in.Model,
		Created:      time.Unix(in.Created, 0),
		FinishReason: finishToCanonical(ch.FinishReason),
		Extensions:   ext,
	}
	if in.Created == 0 {
		resp.Created = time.Now()
	}
	msg := canonical.Message{Role: canonical.RoleAssistant}
	if r := firstNonEmpty(ch.Message.ReasoningContent, ch.Message.Reasoning); r != "" {
		msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartReasoning, Text: r,
			Reasoning: &canonical.ReasoningMeta{Provider: string(protocol.OpenAI)}})
	}
	parts, err := decodeContent(ch.Message.Content, d)
	if err != nil {
		return nil, err
	}
	msg.Content = append(msg.Content, parts...)
	if ch.Message.Refusal != nil && *ch.Message.Refusal != "" {
		msg.Content = append(msg.Content, canonical.Text(*ch.Message.Refusal))
		resp.FinishReason = canonical.FinishContentFilter
	}
	for _, tc := range ch.Message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		msg.Content = append(msg.Content, canonical.ContentPart{
			Type: canonical.PartToolCall,
			ToolCall: &canonical.ToolCall{
				ID: tc.ID, Name: tc.Function.Name, Arguments: args,
				Signature: tc.ExtraContent.Signature(),
			},
		})
	}
	resp.Message = msg
	if in.Usage != nil {
		resp.Usage = usageToCanonical(in.Usage)
	}
	return resp, nil
}

func usageToCanonical(u *wireUsage) canonical.Usage {
	out := canonical.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	return out
}

func usageFromCanonical(u canonical.Usage) *wireUsage {
	w := &wireUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
	if u.ReasoningTokens > 0 {
		w.CompletionTokensDetails = &struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{ReasoningTokens: u.ReasoningTokens}
	}
	if u.CachedInputTokens > 0 {
		w.PromptTokensDetails = &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: u.CachedInputTokens}
	}
	return w
}

// --- response: canonical -> OpenAI ----------------------------------------

func (Codec) EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	protocol.NoteCacheWrite(resp.Usage, d.WithStage("encode:openai"))
	created := resp.Created
	if created.IsZero() {
		created = time.Now()
	}
	model := resp.Model
	if model == "" && req != nil {
		model = req.Model
	}
	out := chatResponse{
		ID:      orDefault(resp.ID, "chatcmpl-"+randomID()),
		Object:  "chat.completion",
		Created: created.Unix(),
		Model:   model,
		Usage:   usageFromCanonical(resp.Usage),
	}
	msg := wireMessage{Role: "assistant"}
	var text strings.Builder
	var reasoning strings.Builder
	for _, p := range resp.Message.Content {
		switch p.Type {
		case canonical.PartText:
			text.WriteString(p.Text)
		case canonical.PartReasoning:
			reasoning.WriteString(p.Text)
		case canonical.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			tc := wireToolCall{ID: orDefault(p.ToolCall.ID, "call_"+randomID()), Type: "function"}
			tc.Function.Name = p.ToolCall.Name
			tc.Function.Arguments = argsString(p.ToolCall.Arguments)
			// The client has to hand this back next turn for a Gemini upstream
			// to accept the call again.
			tc.ExtraContent = protocol.SignatureExtra(p.ToolCall.Signature)
			msg.ToolCalls = append(msg.ToolCalls, tc)
		case canonical.PartNative:
			protocol.NoteNativeContent(p, protocol.OpenAI, "message.content", d)
		}
	}
	msg.Content = mustJSONString(text.String())
	msg.ReasoningContent = reasoning.String()
	out.Choices = []wireChoice{{Index: 0, Message: msg, FinishReason: finishFromCanonical(resp.FinishReason)}}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode openai response: %w", err)
	}
	return protocol.Merge(protocol.OpenAI, resp.Extensions, b, d.WithStage("encode:openai")), nil
}

func (Codec) EncodeError(err *canonical.Error) []byte {
	var out wireError
	out.Error.Message = err.Message
	out.Error.Type = errorTypeString(err.Type)
	out.Error.Param = err.Param
	if err.Code != "" {
		out.Error.Code = err.Code
	}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return []byte(`{"error":{"message":"internal error","type":"internal_error"}}`)
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

// --- shared helpers -------------------------------------------------------

func finishToCanonical(s string) canonical.FinishReason {
	switch s {
	case "stop", "end_turn":
		return canonical.FinishStop
	case "length", "max_tokens":
		return canonical.FinishLength
	case "tool_calls", "function_call":
		return canonical.FinishToolCalls
	case "content_filter":
		return canonical.FinishContentFilter
	default:
		return canonical.FinishUnknown
	}
}

func finishFromCanonical(f canonical.FinishReason) string {
	switch f {
	case canonical.FinishLength:
		return "length"
	case canonical.FinishToolCalls:
		return "tool_calls"
	case canonical.FinishContentFilter:
		return "content_filter"
	case canonical.FinishUnknown:
		return "stop"
	default:
		return "stop"
	}
}

// effortForBudget maps an Anthropic-style token budget onto OpenAI's coarse
// effort levels. The thresholds are a judgement call, hence the lossy note at
// the call site.
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

func argsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func mustJSONString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil { // impossible for a string
		return json.RawMessage(`""`)
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// fileMedia reads OpenAI's document attachment, which is either inline base64
// in a data: URI or a handle only OpenAI can resolve.
func fileMedia(f *filePart) *canonical.Media {
	if f == nil {
		return nil
	}
	switch {
	case f.FileData != "":
		m := protocol.MediaFromURL(f.FileData)
		m.Filename = f.Filename
		if m.MIMEType == "" {
			m.MIMEType = mimeFromName(f.Filename)
		}
		return m
	case f.FileID != "":
		return &canonical.Media{
			FileID:   f.FileID,
			Provider: string(protocol.OpenAI),
			Filename: f.Filename,
		}
	}
	return nil
}

// mimeFromName guesses a document type from its extension. Only the handful of
// document types these APIs accept are worth listing; anything else stays
// empty and the target protocol falls back to its own default.
func mimeFromName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	}
	return ""
}

// encodeMedia renders an attachment as an OpenAI content part.
//
// A remote URL is passed through: OpenAI fetches it itself. A file id is only
// usable if OpenAI issued it — a handle from another provider means nothing
// here, which is the same rule that governs replay tokens and native tools.
func encodeMedia(p canonical.ContentPart, d *canonical.Diagnostics, field string) *contentPart {
	m := p.Media
	if m == nil {
		return nil
	}
	if m.Bound() && !protocol.BoundMediaUsable(protocol.OpenAI, m) {
		protocol.MediaNote(d, field, m,
			"it is a file handle issued by "+m.Provider+", which OpenAI cannot resolve")
		return nil
	}

	if p.Type == canonical.PartImage {
		out := &contentPart{Type: "image_url", ImageURL: &imageURLPart{Detail: m.Detail}}
		switch {
		case m.Inline():
			out.ImageURL.URL = protocol.DataURI(m.MIMEType, m.Data)
		case m.Remote():
			out.ImageURL.URL = m.URL
		default:
			protocol.MediaNote(d, field, m, "an image needs data or a url")
			return nil
		}
		return out
	}

	out := &contentPart{Type: "file", File: &filePart{Filename: m.Filename}}
	switch {
	case m.Inline():
		out.File.FileData = protocol.DataURI(m.MIMEType, m.Data)
	case m.Bound():
		out.File.FileID = m.FileID
	default:
		// OpenAI's file part has no url form.
		protocol.MediaNote(d, field, m, "OpenAI accepts a document inline or by file id, not by url")
		return nil
	}
	return out
}
