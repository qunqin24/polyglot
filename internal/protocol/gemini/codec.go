// Package gemini implements Gemini's native content generation protocol as a
// Polyglot codec: Gemini <-> Canonical, in both directions.
package gemini

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
)

type Codec struct{}

func init() { protocol.Register(Codec{}) }

func (Codec) Name() protocol.Name { return protocol.Gemini }

// --- request: Gemini -> canonical -----------------------------------------

func (Codec) DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error) {
	d = d.WithStage("decode:gemini")

	var in generateRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "invalid JSON body: %v", err)
	}
	if len(in.Contents) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'contents' must not be empty")
	}

	// Gemini carries the model in the URL, so the caller sets it afterwards.
	req := &canonical.Request{IncludeUsage: true}
	// Gemini keeps its parameters inside generationConfig, so capturing only
	// the top level would miss nearly every one of them.
	req.Extensions = protocol.Capture(protocol.Gemini, body,
		protocol.Top(generateRequest{}),
		protocol.Nested("generationConfig", generationConfig{}))

	if in.SystemInstruction != nil {
		for _, p := range in.SystemInstruction.Parts {
			if p.Text != "" {
				req.System = append(req.System, canonical.Text(p.Text))
			}
		}
	}
	if gc := in.GenerationConfig; gc != nil {
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		req.TopK = gc.TopK
		req.MaxTokens = gc.MaxOutputTokens
		req.Stop = gc.StopSequences
		req.PresencePenalty = gc.PresencePenalty
		req.FrequencyPenalty = gc.FrequencyPenalty
		req.Seed = gc.Seed
		req.N = gc.CandidateCount

		switch gc.ResponseMIMEType {
		case "", "text/plain":
		case "application/json":
			rf := &canonical.ResponseFormat{Type: canonical.FormatJSONObject}
			if len(gc.ResponseSchema) > 0 {
				rf.Type = canonical.FormatJSONSchema
				rf.Name = "response"
				rf.Schema = gc.ResponseSchema
			}
			req.ResponseFormat = rf
		default:
			d.Note("generationConfig.responseMimeType", canonical.FidelityUnsupported,
				"response MIME type %q has no equivalent in the other protocols", gc.ResponseMIMEType)
		}

		if tc := gc.ThinkingConfig; tc != nil {
			rc := &canonical.ReasoningConfig{Enabled: true, Visible: tc.IncludeThoughts}
			if tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0 {
				b := *tc.ThinkingBudget
				rc.BudgetTokens = &b
			}
			if tc.ThinkingBudget != nil && *tc.ThinkingBudget == 0 {
				rc.Enabled = false
			}
			req.Reasoning = rc
		}
	}
	// Gemini puts server-side tools and function declarations in the same
	// array, sometimes in the same entry, so the native half is taken from the
	// original bytes: a tool Google ships next year is preserved too, instead
	// of vanishing because this struct has never heard of it.
	rawTools := protocol.RawArray(body, "tools")
	for i, t := range in.Tools {
		if i < len(rawTools) {
			if name, raw := nativeToolPart(rawTools[i]); raw != nil {
				req.NativeTools = req.NativeTools.Add(string(protocol.Gemini), name, raw)
			}
		}
		for _, fd := range t.FunctionDeclarations {
			req.Tools = append(req.Tools, canonical.Tool{
				Name:        fd.Name,
				Description: fd.Description,
				Parameters:  fd.Parameters,
			})
		}
	}
	if in.ToolConfig != nil && in.ToolConfig.FunctionCallingConfig != nil {
		fc := in.ToolConfig.FunctionCallingConfig
		tc := &canonical.ToolChoice{}
		switch strings.ToUpper(fc.Mode) {
		case "", "AUTO":
			tc.Mode = canonical.ToolChoiceAuto
		case "NONE":
			tc.Mode = canonical.ToolChoiceNone
		case "ANY":
			tc.Mode = canonical.ToolChoiceRequired
			if len(fc.AllowedFunctionNames) == 1 {
				tc.Mode = canonical.ToolChoiceSpecific
				tc.Name = fc.AllowedFunctionNames[0]
			} else if len(fc.AllowedFunctionNames) > 1 {
				d.Note("toolConfig.allowedFunctionNames", canonical.FidelityLossy,
					"a restriction to %d specific functions became a plain 'any tool' requirement",
					len(fc.AllowedFunctionNames))
			}
		default:
			return nil, canonical.Errorf(canonical.ErrInvalidRequest,
				"unsupported functionCallingConfig.mode %q", fc.Mode)
		}
		req.ToolChoice = tc
	}

	// Gemini identifies a tool result by function name, not by call id. Ids
	// are synthesised deterministically so calls and results still pair up
	// once converted to a protocol that needs them.
	callSeq := map[string]int{}
	respSeq := map[string]int{}

	for i, c := range in.Contents {
		msg, err := decodeContent(c, d, i, callSeq, respSeq)
		if err != nil {
			return nil, err
		}
		if len(msg.Content) > 0 {
			req.Messages = append(req.Messages, msg)
		}
	}
	if len(req.Messages) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "'contents' contains no usable parts")
	}
	return req, nil
}

func decodeContent(c content, d *canonical.Diagnostics, idx int, callSeq, respSeq map[string]int) (canonical.Message, error) {
	msg := canonical.Message{Role: canonical.RoleUser}
	if c.Role == "model" {
		msg.Role = canonical.RoleAssistant
	}

	for _, p := range c.Parts {
		switch {
		case p.FunctionCall != nil:
			args := p.FunctionCall.Args
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			id := p.FunctionCall.ID
			if id == "" {
				id = synthID(p.FunctionCall.Name, callSeq)
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID: id, Name: p.FunctionCall.Name, Arguments: args,
					Signature: p.ThoughtSignature,
				},
			})

		case p.FunctionResponse != nil:
			id := p.FunctionResponse.ID
			if id == "" {
				id = synthID(p.FunctionResponse.Name, respSeq)
			}
			text := ""
			if len(p.FunctionResponse.Response) > 0 {
				text = unwrapResponse(p.FunctionResponse.Response)
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{
					ToolCallID: id,
					Name:       p.FunctionResponse.Name,
					Content:    []canonical.ContentPart{canonical.Text(text)},
					// Gemini types this field as an object, so the structure is
					// real and worth keeping: sent back as text it would reach
					// the model as a string of JSON rather than as data.
					Structured: p.FunctionResponse.Response,
				},
			})

		case p.InlineData != nil:
			mime := p.InlineData.MIMEType
			kind, ok := protocol.ClassifyMedia(mime)
			if !ok {
				d.Note(fmt.Sprintf("contents[%d]", idx), canonical.FidelityUnsupported,
					"%s content is not converted yet and was not forwarded", mime)
				continue
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type:  kind,
				Media: &canonical.Media{MIMEType: mime, Data: p.InlineData.Data},
			})

		case p.FileData != nil:
			mime := p.FileData.MIMEType
			kind, ok := protocol.ClassifyMedia(mime)
			if !ok {
				d.Note(fmt.Sprintf("contents[%d]", idx), canonical.FidelityUnsupported,
					"%s content is not converted yet and was not forwarded", mime)
				continue
			}
			msg.Content = append(msg.Content, canonical.ContentPart{
				Type: kind,
				Media: &canonical.Media{
					MIMEType: mime,
					FileID:   p.FileData.FileURI,
					Provider: string(protocol.Gemini),
				},
			})

		case p.Thought:
			part := canonical.ContentPart{Type: canonical.PartReasoning, Text: p.Text}
			if p.ThoughtSignature != "" {
				part.Reasoning = &canonical.ReasoningMeta{Signature: p.ThoughtSignature}
			}
			msg.Content = append(msg.Content, part)

		case p.Text != "" || p.ThoughtSignature != "":
			part := canonical.Text(p.Text)
			// Gemini closes a thinking block on the part that follows it,
			// which for a plain answer is this one. The token belongs to the
			// turn, so it has to come back with the turn.
			part.Signature = p.ThoughtSignature
			msg.Content = append(msg.Content, part)
		}
	}

	// A turn made only of function responses is a tool turn.
	if msg.Role == canonical.RoleUser && len(msg.Content) > 0 && allToolResults(msg.Content) {
		msg.Role = canonical.RoleTool
	}
	return msg, nil
}

func allToolResults(parts []canonical.ContentPart) bool {
	for _, p := range parts {
		if p.Type != canonical.PartToolResult {
			return false
		}
	}
	return true
}

// synthID builds a stable identifier from a function name and how many times
// it has been seen, so the n-th call and the n-th response agree.
func synthID(name string, seq map[string]int) string {
	n := seq[name]
	seq[name] = n + 1
	return "call_" + name + "_" + strconv.Itoa(n)
}

// unwrapResponse turns Gemini's functionResponse payload into text. Gemini
// wraps scalar results in an object, commonly under "result" or "output".
func unwrapResponse(raw json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	for _, key := range []string{"result", "output", "content", "response"} {
		if v, ok := obj[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
			return string(v)
		}
	}
	return string(raw)
}

// encodeFunctionResponse writes the payload of a functionResponse part.
//
// Gemini types this field as an object. A result that arrived as one goes back
// unchanged; only a genuinely textual result is wrapped in {"result": ...},
// the shape Gemini's own tooling uses for a scalar. Wrapping an object would
// nest a JSON string inside a JSON object and leave the model reading its own
// tool output as prose.
func encodeFunctionResponse(tr *canonical.ToolResult) (json.RawMessage, error) {
	if len(tr.Structured) > 0 && json.Valid(tr.Structured) {
		if strings.HasPrefix(strings.TrimSpace(string(tr.Structured)), "{") {
			return tr.Structured, nil
		}
	}
	payload, err := json.Marshal(map[string]string{"result": joinText(tr.Content)})
	if err != nil {
		return nil, fmt.Errorf("encode function response: %w", err)
	}
	return payload, nil
}

// --- request: canonical -> Gemini -----------------------------------------

func (Codec) EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	d = d.WithStage("encode:gemini")
	protocol.NoteResponseState(req, protocol.Gemini, d)
	protocol.NoteCacheHints(req, d)

	out := generateRequest{}

	if len(req.System) > 0 {
		out.SystemInstruction = &content{Parts: []part{{Text: joinText(req.System)}}}
	}

	gc := &generationConfig{
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		TopK:             req.TopK,
		MaxOutputTokens:  req.MaxTokens,
		StopSequences:    req.Stop,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Seed:             req.Seed,
		CandidateCount:   req.N,
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case canonical.FormatJSONObject:
			gc.ResponseMIMEType = "application/json"
		case canonical.FormatJSONSchema:
			gc.ResponseMIMEType = "application/json"
			if len(req.ResponseFormat.Schema) > 0 {
				gc.ResponseSchema = req.ResponseFormat.Schema
				// Gemini validates against an OpenAPI 3 subset, so exotic
				// JSON Schema keywords may be rejected upstream.
				d.Note("response_format.schema", canonical.FidelitySemantic,
					"the JSON Schema was passed to Gemini as responseSchema; Gemini accepts an OpenAPI subset, so unusual keywords may be rejected")
			}
		}
	}
	if req.Reasoning != nil {
		tc := &thinkingConfig{IncludeThoughts: req.Reasoning.Visible}
		switch {
		case !req.Reasoning.Enabled:
			zero := 0
			tc.ThinkingBudget = &zero
		case req.Reasoning.BudgetTokens != nil:
			b := *req.Reasoning.BudgetTokens
			tc.ThinkingBudget = &b
		case req.Reasoning.Effort != "":
			b := budgetForEffort(req.Reasoning.Effort)
			tc.ThinkingBudget = &b
			d.Note("reasoning.effort", canonical.FidelityLossy,
				"reasoning_effort=%q was approximated as a thinking budget of %d tokens", req.Reasoning.Effort, b)
		}
		gc.ThinkingConfig = tc
	}
	if !emptyGenerationConfig(gc) {
		out.GenerationConfig = gc
	}

	if len(req.Tools) > 0 {
		decls := make([]functionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, functionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		out.Tools = []wireTool{{FunctionDeclarations: decls}}
	}
	if req.ToolChoice != nil {
		fc := &functionCallingConfig{}
		switch req.ToolChoice.Mode {
		case canonical.ToolChoiceAuto:
			fc.Mode = "AUTO"
		case canonical.ToolChoiceNone:
			fc.Mode = "NONE"
		case canonical.ToolChoiceRequired:
			fc.Mode = "ANY"
		case canonical.ToolChoiceSpecific:
			fc.Mode = "ANY"
			fc.AllowedFunctionNames = []string{req.ToolChoice.Name}
		}
		out.ToolConfig = &toolConfig{FunctionCallingConfig: fc}
		if req.ToolChoice.ParallelDisabled {
			d.Note("tool_choice.parallel", canonical.FidelityUnsupported,
				"Gemini has no way to disable parallel tool calls")
		}
	}

	contents, err := encodeContents(req.Messages, d)
	if err != nil {
		return nil, err
	}
	out.Contents = contents
	if len(out.Contents) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request has no messages")
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode gemini request: %w", err)
	}
	b = protocol.MergeNativeTools(protocol.Gemini, req.NativeTools, b, d)
	return protocol.Merge(protocol.Gemini, req.Extensions, b, d), nil
}

// encodeContents maps canonical turns onto Gemini's user/model alternation,
// merging consecutive turns that would share a role.
func encodeContents(msgs []canonical.Message, d *canonical.Diagnostics) ([]content, error) {
	// Gemini matches a function response to its call by name, so the name of
	// each call has to be remembered even when the client only sent an id.
	nameByID := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Type == canonical.PartToolCall && p.ToolCall != nil {
				nameByID[p.ToolCall.ID] = p.ToolCall.Name
			}
		}
	}

	var out []content
	push := func(role string, parts []part) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			return
		}
		out = append(out, content{Role: role, Parts: parts})
	}

	for i, m := range msgs {
		var parts []part
		signed := signedThoughtRuns(m.Content)
		for pi, p := range m.Content {
			switch p.Type {
			case canonical.PartText:
				// A signature-only text part is not empty. Gemini can put the
				// replay token in a final streamed part whose text is "".
				if p.Text != "" || p.Signature != "" {
					parts = append(parts, part{Text: p.Text, ThoughtSignature: p.Signature})
				}
			case canonical.PartImage, canonical.PartFile:
				if mp := encodeMediaPart(p, d, fmt.Sprintf("contents[%d]", i)); mp != nil {
					parts = append(parts, *mp)
				}
			case canonical.PartReasoning:
				if m.Role != canonical.RoleAssistant {
					continue
				}
				if signed[pi] {
					pt := part{Text: p.Text, Thought: true}
					if p.Reasoning != nil {
						pt.ThoughtSignature = p.Reasoning.Signature
					}
					if pt.Text != "" || pt.ThoughtSignature != "" {
						parts = append(parts, pt)
					}
					continue
				}
				// Gemini rejects a thinking block that carries no signature of
				// its own, so reasoning from another provider cannot be sent.
				// One note per block, not per fragment: the block is what was
				// lost, and a long thought would otherwise report itself a
				// dozen times over.
				if pi == 0 || m.Content[pi-1].Type != canonical.PartReasoning {
					d.Note(fmt.Sprintf("messages[%d].reasoning", i), canonical.FidelityLossy,
						"assistant reasoning was omitted: Gemini only accepts thought parts carrying its own signature")
				}
			case canonical.PartToolCall:
				if p.ToolCall == nil {
					continue
				}
				args := p.ToolCall.Arguments
				if len(args) == 0 || !json.Valid(args) {
					if len(args) > 0 {
						d.Note(fmt.Sprintf("messages[%d].tool_call", i), canonical.FidelityLossy,
							"tool call %q had unparseable arguments; an empty object was sent", p.ToolCall.Name)
					}
					args = json.RawMessage("{}")
				}
				parts = append(parts, part{
					FunctionCall:     &functionCall{Name: p.ToolCall.Name, Args: args},
					ThoughtSignature: p.ToolCall.Signature,
				})
			case canonical.PartToolResult:
				if p.ToolResult == nil {
					continue
				}
				name := p.ToolResult.Name
				if name == "" {
					name = nameByID[p.ToolResult.ToolCallID]
				}
				if name == "" {
					d.Note(fmt.Sprintf("messages[%d].tool_result", i), canonical.FidelityLossy,
						"a tool result had no matching tool call, so Gemini cannot be told which function it answers")
					name = "unknown_function"
				}
				payload, err := encodeFunctionResponse(p.ToolResult)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part{FunctionResponse: &functionResponse{Name: name, Response: payload}})
			case canonical.PartNative:
				protocol.NoteNativeContent(p, protocol.Gemini, fmt.Sprintf("contents[%d].parts", i), d)
			}
		}
		if len(parts) == 0 {
			continue
		}

		switch m.Role {
		case canonical.RoleAssistant:
			signFunctionCalls(parts, i, d)
			push("model", parts)
		case canonical.RoleSystem:
			d.Note(fmt.Sprintf("messages[%d]", i), canonical.FidelitySemantic,
				"a system message inside the conversation was sent as a user turn")
			push("user", parts)
		default: // user and tool both map onto Gemini's user role
			push("user", parts)
		}
	}
	return out, nil
}

// placeholderSignature is Google's documented escape hatch for replaying
// function-call history that genuinely has no signature — history that came
// from another provider, or from a Gemini version that predates signatures.
const placeholderSignature = "skip_thought_signature_validator"

// signFunctionCalls satisfies Gemini 3's rule that the first functionCall part
// of each model step must carry a thoughtSignature; a request that has lost it
// is rejected with 400 rather than degraded. Later parallel calls are not
// validated, so they are left exactly as they arrived.
//
// A real signature is always preferred. The placeholder is only reached when
// the calls were produced by a provider that never issued one, which is the
// normal case when a conversation is being converted into Gemini's protocol.
func signFunctionCalls(parts []part, idx int, d *canonical.Diagnostics) {
	for i, p := range parts {
		if p.FunctionCall == nil {
			continue
		}
		if p.ThoughtSignature == "" {
			parts[i].ThoughtSignature = placeholderSignature
			d.Note(fmt.Sprintf("messages[%d].tool_call.signature", idx), canonical.FidelitySemantic,
				"tool call %q carried no Gemini thought signature, so the documented placeholder "+
					"was sent; Gemini rejects unsigned function calls outright, but the model's "+
					"original reasoning state cannot be restored", p.FunctionCall.Name)
		}
		return // only the first functionCall part of a step is validated
	}
}

func emptyGenerationConfig(gc *generationConfig) bool {
	return gc.Temperature == nil && gc.TopP == nil && gc.TopK == nil && gc.CandidateCount == nil &&
		gc.MaxOutputTokens == nil && len(gc.StopSequences) == 0 && gc.PresencePenalty == nil &&
		gc.FrequencyPenalty == nil && gc.Seed == nil && gc.ResponseMIMEType == "" &&
		len(gc.ResponseSchema) == 0 && gc.ThinkingConfig == nil
}

// signedThoughtRuns reports, for each reasoning part, whether its model turn
// carries a Gemini replay signature.
//
// Gemini signs a model response, not every thought fragment. The token may be
// on a thought part, the following answer text (including an empty final text
// part), or a function call. Judging only the thought parts throws away a turn
// that is perfectly replayable. A canonical Message is one model response, so
// a signature anywhere in it authorises replaying that response's thought
// parts in their original order.
func signedThoughtRuns(parts []canonical.ContentPart) []bool {
	signed := make([]bool, len(parts))
	hasSignature := false
	for _, p := range parts {
		switch p.Type {
		case canonical.PartText, canonical.PartImage, canonical.PartFile:
			hasSignature = hasSignature || p.Signature != ""
		case canonical.PartReasoning:
			hasSignature = hasSignature || p.Reasoning != nil && p.Reasoning.Signature != ""
		case canonical.PartToolCall:
			hasSignature = hasSignature || p.ToolCall != nil && p.ToolCall.Signature != ""
		}
	}
	if !hasSignature {
		return signed
	}
	for i, p := range parts {
		if p.Type == canonical.PartReasoning {
			signed[i] = true
		}
	}
	return signed
}

// --- response -------------------------------------------------------------

func (Codec) DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error) {
	d = d.WithStage("decode:gemini")

	var in generateResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream returned invalid JSON: %v", err)
	}
	if len(in.Candidates) == 0 {
		if in.PromptFeedback != nil && in.PromptFeedback.BlockReason != "" {
			return nil, canonical.Errorf(canonical.ErrInvalidRequest,
				"Gemini blocked the prompt: %s", in.PromptFeedback.BlockReason)
		}
		return nil, canonical.Errorf(canonical.ErrUpstream, "upstream response contains no candidates")
	}
	if len(in.Candidates) > 1 {
		d.Note("candidates", canonical.FidelityLossy,
			"upstream returned %d candidates; only the first is used", len(in.Candidates))
	}
	cand := in.Candidates[0]

	resp := &canonical.Response{
		ID:      orDefault(in.ResponseID, "gen-"+idgen.New()),
		Model:   in.ModelVersion,
		Created: parseCreateTime(in.CreateTime),
		// groundingMetadata — the citations that make a web-search answer
		// checkable — lives on the candidate, not at the top level, so the
		// first candidate is captured too. It is the half of the search
		// feature the caller can actually see.
		Extensions: protocol.Capture(protocol.Gemini, body,
			protocol.Top(generateResponse{}),
			protocol.Nested("candidates.0", candidate{})),
	}
	msg := canonical.Message{Role: canonical.RoleAssistant}
	callSeq := map[string]int{}
	hasToolCall := false
	if cand.Content != nil {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				hasToolCall = true
				args := p.FunctionCall.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				id := p.FunctionCall.ID
				if id == "" {
					id = synthID(p.FunctionCall.Name, callSeq)
				}
				msg.Content = append(msg.Content, canonical.ContentPart{
					Type: canonical.PartToolCall,
					ToolCall: &canonical.ToolCall{
						ID: id, Name: p.FunctionCall.Name, Arguments: args,
						Signature: p.ThoughtSignature,
					},
				})
			case p.Thought:
				part := canonical.ContentPart{Type: canonical.PartReasoning, Text: p.Text}
				if p.ThoughtSignature != "" {
					part.Reasoning = &canonical.ReasoningMeta{Signature: p.ThoughtSignature}
				}
				msg.Content = append(msg.Content, part)
			case p.Text != "" || p.ThoughtSignature != "":
				part := canonical.Text(p.Text)
				part.Signature = p.ThoughtSignature
				msg.Content = append(msg.Content, part)
			case p.InlineData != nil || p.FileData != nil:
				d.Note("candidates[0]", canonical.FidelityUnsupported,
					"the model returned inline data, which Polyglot does not convert yet")
			}
		}
	}
	resp.Message = msg
	// Gemini has no tool-call finish reason: it reports STOP and includes
	// functionCall parts. Other protocols need the distinction.
	resp.FinishReason = finishToCanonical(cand.FinishReason, hasToolCall)
	if in.UsageMetadata != nil {
		resp.Usage = usageToCanonical(in.UsageMetadata)
	}
	return resp, nil
}

func (Codec) EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error) {
	protocol.NoteCacheWrite(resp.Usage, d.WithStage("encode:gemini"))
	model := resp.Model
	if model == "" && req != nil {
		model = req.Model
	}
	created := resp.Created
	if created.IsZero() {
		created = time.Now()
	}
	out := generateResponse{
		ResponseID:   orDefault(resp.ID, "gen-"+idgen.New()),
		ModelVersion: model,
		CreateTime:   created.UTC().Format(time.RFC3339Nano),
		UsageMetadata: &usageMetadata{
			PromptTokenCount:        resp.Usage.InputTokens,
			CandidatesTokenCount:    resp.Usage.OutputTokens,
			TotalTokenCount:         resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ThoughtsTokenCount:      resp.Usage.ReasoningTokens,
			CachedContentTokenCount: resp.Usage.CachedInputTokens,
		},
	}
	c := &content{Role: "model"}
	for _, p := range resp.Message.Content {
		switch p.Type {
		case canonical.PartText:
			if p.Text != "" || p.Signature != "" {
				c.Parts = append(c.Parts, part{Text: p.Text, ThoughtSignature: p.Signature})
			}
		case canonical.PartReasoning:
			pt := part{Text: p.Text, Thought: true}
			if p.Reasoning != nil {
				pt.ThoughtSignature = p.Reasoning.Signature
			}
			c.Parts = append(c.Parts, pt)
		case canonical.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			args := p.ToolCall.Arguments
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			c.Parts = append(c.Parts, part{
				FunctionCall: &functionCall{
					ID: p.ToolCall.ID, Name: p.ToolCall.Name, Args: args,
				},
				// A Gemini client has to echo this back on the next turn.
				ThoughtSignature: p.ToolCall.Signature,
			})
		case canonical.PartNative:
			protocol.NoteNativeContent(p, protocol.Gemini, "message.content", d)
		}
	}
	if c.Parts == nil {
		c.Parts = []part{}
	}
	out.Candidates = []candidate{{Content: c, FinishReason: finishFromCanonical(resp.FinishReason), Index: 0}}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode gemini response: %w", err)
	}
	return protocol.Merge(protocol.Gemini, resp.Extensions, b, d.WithStage("encode:gemini")), nil
}

func (Codec) EncodeError(err *canonical.Error) []byte {
	var out wireError
	out.Error.Code = err.Status()
	out.Error.Message = err.Message
	out.Error.Status = statusString(err.Type)
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return []byte(`{"error":{"code":500,"message":"internal error","status":"INTERNAL"}}`)
	}
	return b
}

func statusString(t canonical.ErrorType) string {
	switch t {
	case canonical.ErrInvalidRequest, canonical.ErrUnsupported:
		return "INVALID_ARGUMENT"
	case canonical.ErrAuthentication:
		return "UNAUTHENTICATED"
	case canonical.ErrPermission:
		return "PERMISSION_DENIED"
	case canonical.ErrNotFound:
		return "NOT_FOUND"
	case canonical.ErrRateLimit:
		return "RESOURCE_EXHAUSTED"
	case canonical.ErrOverloaded:
		return "UNAVAILABLE"
	case canonical.ErrTimeout:
		return "DEADLINE_EXCEEDED"
	default:
		return "INTERNAL"
	}
}

// --- helpers --------------------------------------------------------------

func usageToCanonical(u *usageMetadata) canonical.Usage {
	out := canonical.Usage{
		InputTokens:       u.PromptTokenCount,
		OutputTokens:      u.CandidatesTokenCount,
		ReasoningTokens:   u.ThoughtsTokenCount,
		CachedInputTokens: u.CachedContentTokenCount,
	}
	// Gemini counts thoughts separately from candidate tokens; the other
	// protocols include reasoning in the output count.
	out.OutputTokens += u.ThoughtsTokenCount
	return out
}

func finishToCanonical(s string, hasToolCall bool) canonical.FinishReason {
	switch strings.ToUpper(s) {
	case "STOP", "":
		if hasToolCall {
			return canonical.FinishToolCalls
		}
		return canonical.FinishStop
	case "MAX_TOKENS":
		return canonical.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return canonical.FinishContentFilter
	case "MALFORMED_FUNCTION_CALL":
		return canonical.FinishToolCalls
	default:
		return canonical.FinishStop
	}
}

func finishFromCanonical(f canonical.FinishReason) string {
	switch f {
	case canonical.FinishLength:
		return "MAX_TOKENS"
	case canonical.FinishContentFilter:
		return "SAFETY"
	default:
		// Gemini reports STOP for a completed turn, tool calls included.
		return "STOP"
	}
}

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

// parseCreateTime reads Gemini's RFC 3339 response timestamp. An upstream that
// omits it, or spells it in a way this cannot read, gets the local clock: the
// field is only ever descriptive metadata, so guessing is better than failing
// a response over it.
func parseCreateTime(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}

// nativeToolPart splits a Gemini tools entry into the part Polyglot converts
// and the part it cannot. functionDeclarations become canonical tools; every
// other member — googleSearch, codeExecution, urlContext, whatever Google adds
// next — is a server-side tool that only Gemini can run, so it is kept
// verbatim and replayed to a Gemini upstream.
//
// Reading it from the raw entry rather than a typed struct is what makes a
// tool this codec has never heard of survive instead of disappearing.
func nativeToolPart(raw json.RawMessage) (string, json.RawMessage) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", nil
	}
	delete(entry, "functionDeclarations")
	if len(entry) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(entry))
	for k := range entry {
		names = append(names, k)
	}
	sort.Strings(names)

	out, err := json.Marshal(entry)
	if err != nil {
		return "", nil
	}
	return strings.Join(names, "+"), out
}

// encodeMediaPart renders an attachment as a Gemini part.
//
// Gemini is the one protocol here that will not fetch a URL for you: it takes
// bytes inline, or a URI for a file already uploaded to Google. A remote URL
// therefore cannot be forwarded as-is, and is reported unless Polyglot was
// configured to download and inline it before this point.
func encodeMediaPart(p canonical.ContentPart, d *canonical.Diagnostics, field string) *part {
	m := p.Media
	if m == nil {
		return nil
	}
	switch {
	case m.Inline():
		if m.Detail != "" {
			d.Note(field, canonical.FidelityLossy,
				"the image detail hint %q was dropped: Gemini has no equivalent", m.Detail)
		}
		return &part{InlineData: &inlineData{MIMEType: m.MIMEType, Data: m.Data}}

	case protocol.BoundMediaUsable(protocol.Gemini, m):
		return &part{FileData: &fileData{MIMEType: m.MIMEType, FileURI: m.FileID}}

	case m.Bound():
		protocol.MediaNote(d, field, m,
			"it is a file handle issued by "+m.Provider+", which Gemini cannot resolve")
		return nil

	case m.Remote():
		protocol.MediaNote(d, field, m,
			"Gemini does not fetch remote URLs; send the bytes inline, "+
				"or set FETCH_REMOTE_MEDIA=true to have Polyglot download it")
		return nil
	}
	protocol.MediaNote(d, field, m, "it carried no data, url or file id")
	return nil
}
