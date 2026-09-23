// Package live implements Gemini's bidirectional Live JSON messages. A Live
// session is not a request/response or SSE stream, so it has a session codec
// instead of implementing the stateless protocol.Codec interface.
package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

const Name protocol.Name = "gemini-live"

type envelope struct {
	Setup                   *setup          `json:"setup,omitempty"`
	ClientContent           *clientContent  `json:"clientContent,omitempty"`
	RealtimeInput           *realtimeInput  `json:"realtimeInput,omitempty"`
	ToolResponse            *toolResponse   `json:"toolResponse,omitempty"`
	SetupComplete           json.RawMessage `json:"setupComplete,omitempty"`
	ServerContent           *serverContent  `json:"serverContent,omitempty"`
	ToolCall                *toolCall       `json:"toolCall,omitempty"`
	ToolCallCancellation    *toolCancel     `json:"toolCallCancellation,omitempty"`
	GoAway                  *goAway         `json:"goAway,omitempty"`
	SessionResumptionUpdate *resume         `json:"sessionResumptionUpdate,omitempty"`
	UsageMetadata           *usage          `json:"usageMetadata,omitempty"`
}

type setup struct {
	Model                    string          `json:"model"`
	GenerationConfig         *generation     `json:"generationConfig,omitempty"`
	SystemInstruction        *content        `json:"systemInstruction,omitempty"`
	InputAudioTranscription  json.RawMessage `json:"inputAudioTranscription,omitempty"`
	OutputAudioTranscription json.RawMessage `json:"outputAudioTranscription,omitempty"`
}

type generation struct {
	ResponseModalities []string `json:"responseModalities,omitempty"`
}

type clientContent struct {
	Turns        []content `json:"turns,omitempty"`
	TurnComplete bool      `json:"turnComplete,omitempty"`
}

type content struct {
	Role  string            `json:"role,omitempty"`
	Parts []json.RawMessage `json:"parts,omitempty"`
}

type realtimeInput struct {
	Text           *string         `json:"text,omitempty"`
	Audio          *blob           `json:"audio,omitempty"`
	Video          *blob           `json:"video,omitempty"`
	AudioStreamEnd bool            `json:"audioStreamEnd,omitempty"`
	ActivityStart  json.RawMessage `json:"activityStart,omitempty"`
	ActivityEnd    json.RawMessage `json:"activityEnd,omitempty"`
}

type blob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type toolResponse struct {
	FunctionResponses []functionResponse `json:"functionResponses,omitempty"`
}

type functionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type toolCall struct {
	FunctionCalls []functionCall `json:"functionCalls,omitempty"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type serverContent struct {
	ModelTurn           *content    `json:"modelTurn,omitempty"`
	InputTranscription  *transcript `json:"inputTranscription,omitempty"`
	OutputTranscription *transcript `json:"outputTranscription,omitempty"`
	GenerationComplete  bool        `json:"generationComplete,omitempty"`
	TurnComplete        bool        `json:"turnComplete,omitempty"`
	Interrupted         bool        `json:"interrupted,omitempty"`
	InteractionStatus   string      `json:"interactionStatus,omitempty"`
}

type transcript struct {
	Text         string `json:"text,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
}

type toolCancel struct {
	IDs []string `json:"ids"`
}
type goAway struct {
	TimeLeft string `json:"timeLeft,omitempty"`
}
type resume struct {
	NewHandle string `json:"newHandle,omitempty"`
	Resumable bool   `json:"resumable,omitempty"`
}

type usage struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	ResponseTokenCount      int `json:"responseTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
}

// DecodeClient and DecodeServer accept only the messages legal in their
// direction. Malformed or unrepresentable data closes a session instead of
// quietly losing content. Each frame becomes a fresh canonical LiveMessage.
func DecodeClient(raw []byte) (*canonical.LiveMessage, error) { return decode(raw, true) }
func DecodeServer(raw []byte) (*canonical.LiveMessage, error) { return decode(raw, false) }

func decode(raw []byte, client bool) (*canonical.LiveMessage, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid Live JSON")
	}
	var w envelope
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("decode Live message: %w", err)
	}
	m := &canonical.LiveMessage{}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return nil, fmt.Errorf("Live message must be an object")
	}
	count := 0
	for _, name := range []string{"setup", "clientContent", "realtimeInput", "toolResponse", "setupComplete", "serverContent", "toolCall", "toolCallCancellation", "goAway", "sessionResumptionUpdate"} {
		if _, ok := root[name]; ok {
			count++
		}
	}
	if count > 1 {
		return nil, fmt.Errorf("Live message contains multiple message types")
	}
	switch {
	case w.Setup != nil && client:
		m.Kind = canonical.LiveSetup
		m.Model = strings.TrimPrefix(w.Setup.Model, "models/")
		if m.Model == "" {
			return nil, fmt.Errorf("Live setup requires a model")
		}
		m.Options = &canonical.LiveOptions{}
		if w.Setup.GenerationConfig != nil {
			m.Options.ResponseModalities = w.Setup.GenerationConfig.ResponseModalities
		}
		if w.Setup.SystemInstruction != nil {
			parts, err := decodeParts(w.Setup.SystemInstruction.Parts)
			if err != nil {
				return nil, fmt.Errorf("decode system instruction: %w", err)
			}
			m.Options.System = parts
			m.Options.SystemRole = w.Setup.SystemInstruction.Role
			m.Options.HasSystem = true
		}
		m.Options.InputTranscription = w.Setup.InputAudioTranscription
		m.Options.OutputTranscription = w.Setup.OutputAudioTranscription
	case w.ClientContent != nil && client:
		m.Kind = canonical.LiveClientContent
		m.Content = &canonical.LiveContent{TurnComplete: w.ClientContent.TurnComplete}
		for _, turn := range w.ClientContent.Turns {
			parts, err := decodeParts(turn.Parts)
			if err != nil {
				return nil, fmt.Errorf("decode Live turn: %w", err)
			}
			role := canonical.RoleUser
			if turn.Role == "model" {
				role = canonical.RoleAssistant
			}
			if turn.Role != "" && turn.Role != "user" && turn.Role != "model" {
				return nil, fmt.Errorf("unsupported Live role %q", turn.Role)
			}
			m.Content.Turns = append(m.Content.Turns, canonical.Message{Role: role, Content: parts})
		}
	case w.RealtimeInput != nil && client:
		m.Kind = canonical.LiveRealtimeInput
		r := w.RealtimeInput
		m.Realtime = &canonical.LiveRealtime{Text: r.Text, AudioStreamEnd: r.AudioStreamEnd,
			ActivityStart: len(r.ActivityStart) > 0, ActivityEnd: len(r.ActivityEnd) > 0}
		if r.Audio != nil {
			m.Realtime.Audio = &canonical.Media{MIMEType: r.Audio.MIMEType, Data: r.Audio.Data}
		}
		if r.Video != nil {
			m.Realtime.Video = &canonical.Media{MIMEType: r.Video.MIMEType, Data: r.Video.Data}
		}
	case w.ToolResponse != nil && client:
		m.Kind = canonical.LiveToolResponse
		for _, f := range w.ToolResponse.FunctionResponses {
			m.ToolResponses = append(m.ToolResponses, canonical.LiveFunctionResponse{ID: f.ID, Name: f.Name, Response: f.Response})
		}
	case len(w.SetupComplete) > 0 && !client:
		m.Kind = canonical.LiveSetupComplete
	case w.ServerContent != nil && !client:
		m.Kind = canonical.LiveServerContent
		s := w.ServerContent
		m.Output = &canonical.LiveOutput{GenerationComplete: s.GenerationComplete,
			TurnComplete: s.TurnComplete, Interrupted: s.Interrupted, InteractionStatus: s.InteractionStatus}
		if s.ModelTurn != nil {
			m.Output.ModelRole = s.ModelTurn.Role
			m.Output.HasModelTurn = true
			parts, err := decodeParts(s.ModelTurn.Parts)
			if err != nil {
				return nil, fmt.Errorf("decode model turn: %w", err)
			}
			m.Output.Parts = parts
		}
		if s.InputTranscription != nil {
			m.Output.InputTranscript, m.Output.InputLanguage = s.InputTranscription.Text, s.InputTranscription.LanguageCode
		}
		if s.OutputTranscription != nil {
			m.Output.OutputTranscript, m.Output.OutputLanguage = s.OutputTranscription.Text, s.OutputTranscription.LanguageCode
		}
	case w.ToolCall != nil && !client:
		m.Kind = canonical.LiveToolCall
		for _, f := range w.ToolCall.FunctionCalls {
			m.ToolCalls = append(m.ToolCalls, canonical.ToolCall{ID: f.ID, Name: f.Name, Arguments: f.Args})
		}
	case w.ToolCallCancellation != nil && !client:
		m.Kind = canonical.LiveToolCancel
		m.CancelledToolIDs = w.ToolCallCancellation.IDs
	case w.GoAway != nil && !client:
		m.Kind = canonical.LiveGoAway
		m.GoAwayTimeLeft = w.GoAway.TimeLeft
	case w.SessionResumptionUpdate != nil && !client:
		m.Kind = canonical.LiveResume
		m.ResumeHandle = w.SessionResumptionUpdate.NewHandle
		m.Resumable = w.SessionResumptionUpdate.Resumable
	case count == 0 && w.UsageMetadata != nil && !client:
		m.Kind = canonical.LiveUsage
	case count == 0 && len(root) == 1:
		for name := range root {
			m.Kind = canonical.LiveNativeEvent
			m.NativeEvent = &canonical.NativeContent{Protocol: string(Name), Type: name, Raw: append([]byte(nil), raw...)}
		}
	default:
		return nil, fmt.Errorf("unsupported Live message type")
	}
	if w.UsageMetadata != nil {
		m.Usage = &canonical.Usage{InputTokens: w.UsageMetadata.PromptTokenCount,
			OutputTokens:      w.UsageMetadata.ResponseTokenCount,
			CachedInputTokens: w.UsageMetadata.CachedContentTokenCount,
			ReasoningTokens:   w.UsageMetadata.ThoughtsTokenCount}
	}
	// An extension is replayed only to this dialect. Scopes include the nested
	// configuration and controls; unknown content *parts* become PartNative.
	scopes := []protocol.Scope{
		protocol.Top(envelope{}), protocol.Nested("setup", setup{}),
		protocol.Nested("setupComplete", struct{}{}),
		protocol.Nested("setup.generationConfig", generation{}),
		protocol.Nested("setup.systemInstruction", content{}),
		protocol.Nested("realtimeInput", realtimeInput{}),
		protocol.Nested("realtimeInput.audio", blob{}), protocol.Nested("realtimeInput.video", blob{}),
		protocol.Nested("clientContent", clientContent{}),
		protocol.Nested("toolResponse", toolResponse{}), protocol.Nested("toolCall", toolCall{}),
		protocol.Nested("toolCallCancellation", toolCancel{}),
		protocol.Nested("serverContent", serverContent{}),
		protocol.Nested("serverContent.modelTurn", content{}),
		protocol.Nested("serverContent.inputTranscription", transcript{}),
		protocol.Nested("serverContent.outputTranscription", transcript{}),
		protocol.Nested("goAway", goAway{}), protocol.Nested("sessionResumptionUpdate", resume{}),
		protocol.Nested("usageMetadata", usage{}),
	}
	if w.ClientContent != nil {
		for i := range w.ClientContent.Turns {
			scopes = append(scopes, protocol.Nested(fmt.Sprintf("clientContent.turns.%d", i), content{}))
		}
	}
	if w.ToolCall != nil {
		for i := range w.ToolCall.FunctionCalls {
			scopes = append(scopes, protocol.Nested(fmt.Sprintf("toolCall.functionCalls.%d", i), functionCall{}))
		}
	}
	if w.ToolResponse != nil {
		for i := range w.ToolResponse.FunctionResponses {
			scopes = append(scopes, protocol.Nested(fmt.Sprintf("toolResponse.functionResponses.%d", i), functionResponse{}))
		}
	}
	m.Extensions = protocol.Capture(Name, raw, scopes...)
	return m, nil
}

func decodeParts(parts []json.RawMessage) ([]canonical.ContentPart, error) {
	out := make([]canonical.ContentPart, 0, len(parts))
	for _, raw := range parts {
		var p struct {
			Text             string            `json:"text"`
			InlineData       *blob             `json:"inlineData"`
			Thought          bool              `json:"thought"`
			ThoughtSignature string            `json:"thoughtSignature"`
			FunctionCall     *functionCall     `json:"functionCall"`
			FunctionResponse *functionResponse `json:"functionResponse"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		payloads := 0
		// The nested shape is provider-bound. Preserve it as native content if
		// it contains anything this codec cannot express precisely.
		for name := range obj {
			switch name {
			case "text", "inlineData", "thought", "thoughtSignature", "functionCall", "functionResponse":
			default:
				out = append(out, canonical.ContentPart{Type: canonical.PartNative,
					Native: &canonical.NativeContent{Protocol: string(Name), Type: "live.part", Raw: raw}})
				goto next
			}
		}
		if _, ok := obj["text"]; ok {
			payloads++
		}
		if p.InlineData != nil {
			payloads++
		}
		if p.FunctionCall != nil {
			payloads++
		}
		if p.FunctionResponse != nil {
			payloads++
		}
		if payloads > 1 || (p.FunctionResponse != nil && p.ThoughtSignature != "") {
			out = append(out, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(Name), Type: "live.part", Raw: raw}})
			continue
		}
		if (p.InlineData != nil && hasExtra(obj["inlineData"], "mimeType", "data")) ||
			(p.FunctionCall != nil && hasExtra(obj["functionCall"], "id", "name", "args")) ||
			(p.FunctionResponse != nil && hasExtra(obj["functionResponse"], "id", "name", "response")) {
			out = append(out, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(Name), Type: "live.part", Raw: raw}})
			continue
		}
		switch {
		case p.InlineData != nil:
			kind := canonical.PartImage
			if strings.HasPrefix(p.InlineData.MIMEType, "audio/") {
				kind = canonical.PartAudio
			} else if !strings.HasPrefix(p.InlineData.MIMEType, "image/") {
				out = append(out, canonical.ContentPart{Type: canonical.PartNative,
					Native: &canonical.NativeContent{Protocol: string(Name), Type: "live.part", Raw: raw}})
				continue
			}
			out = append(out, canonical.ContentPart{Type: kind,
				Media: &canonical.Media{MIMEType: p.InlineData.MIMEType, Data: p.InlineData.Data}, Signature: p.ThoughtSignature})
		case p.FunctionCall != nil:
			out = append(out, canonical.ContentPart{Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Arguments: p.FunctionCall.Args, Signature: p.ThoughtSignature}})
		case p.FunctionResponse != nil:
			out = append(out, canonical.ContentPart{Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{ToolCallID: p.FunctionResponse.ID, Name: p.FunctionResponse.Name, Structured: p.FunctionResponse.Response}})
		case p.Text != "" || p.Thought:
			kind := canonical.PartText
			if p.Thought {
				kind = canonical.PartReasoning
			}
			out = append(out, canonical.ContentPart{Type: kind, Text: p.Text, Signature: p.ThoughtSignature})
		default:
			out = append(out, canonical.ContentPart{Type: canonical.PartNative,
				Native: &canonical.NativeContent{Protocol: string(Name), Type: "live.part", Raw: raw}})
		}
	next:
	}
	return out, nil
}

func hasExtra(raw json.RawMessage, allowed ...string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return true
	}
	for name := range obj {
		found := false
		for _, key := range allowed {
			if name == key {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func encodeParts(parts []canonical.ContentPart, d *canonical.Diagnostics) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		var obj any
		switch p.Type {
		case canonical.PartText, canonical.PartReasoning:
			v := map[string]any{"text": p.Text}
			if p.Type == canonical.PartReasoning {
				v["thought"] = true
			}
			if p.Signature != "" {
				v["thoughtSignature"] = p.Signature
			}
			obj = v
		case canonical.PartAudio, canonical.PartImage:
			if p.Media == nil {
				return nil, fmt.Errorf("Live media part has no data")
			}
			v := map[string]any{"inlineData": blob{MIMEType: p.Media.MIMEType, Data: p.Media.Data}}
			if p.Signature != "" {
				v["thoughtSignature"] = p.Signature
			}
			obj = v
		case canonical.PartToolCall:
			if p.ToolCall == nil {
				return nil, fmt.Errorf("Live tool call is empty")
			}
			v := map[string]any{"functionCall": functionCall{ID: p.ToolCall.ID, Name: p.ToolCall.Name, Args: p.ToolCall.Arguments}}
			if p.ToolCall.Signature != "" {
				v["thoughtSignature"] = p.ToolCall.Signature
			}
			obj = v
		case canonical.PartToolResult:
			if p.ToolResult == nil {
				return nil, fmt.Errorf("Live tool result is empty")
			}
			obj = map[string]any{"functionResponse": functionResponse{ID: p.ToolResult.ToolCallID, Name: p.ToolResult.Name, Response: p.ToolResult.Structured}}
		case canonical.PartNative:
			if p.Native == nil {
				return nil, fmt.Errorf("Live native part is empty")
			}
			if p.Native.Protocol != string(Name) {
				d.Note("content", canonical.FidelityUnsupported, "provider-bound %s content cannot be sent to Gemini Live", p.Native.Protocol)
				continue
			}
			out = append(out, append(json.RawMessage(nil), p.Native.Raw...))
			continue
		default:
			return nil, fmt.Errorf("unsupported Live content part %q", p.Type)
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func EncodeClient(m *canonical.LiveMessage, d *canonical.Diagnostics) ([]byte, error) {
	return encode(m, d, true)
}
func EncodeServer(m *canonical.LiveMessage, d *canonical.Diagnostics) ([]byte, error) {
	return encode(m, d, false)
}

func encode(m *canonical.LiveMessage, d *canonical.Diagnostics, client bool) ([]byte, error) {
	w := envelope{}
	switch m.Kind {
	case canonical.LiveSetup:
		if !client || m.Options == nil || m.Model == "" {
			return nil, fmt.Errorf("invalid Live setup")
		}
		w.Setup = &setup{Model: "models/" + strings.TrimPrefix(m.Model, "models/")}
		// An empty config object is needed to replay unknown generationConfig
		// parameters (voice, temperature, and future vendor settings).
		w.Setup.GenerationConfig = &generation{ResponseModalities: m.Options.ResponseModalities}
		if m.Options.HasSystem {
			parts, err := encodeParts(m.Options.System, d)
			if err != nil {
				return nil, err
			}
			w.Setup.SystemInstruction = &content{Role: m.Options.SystemRole, Parts: parts}
		}
		w.Setup.InputAudioTranscription = m.Options.InputTranscription
		w.Setup.OutputAudioTranscription = m.Options.OutputTranscription
	case canonical.LiveClientContent:
		if !client || m.Content == nil {
			return nil, fmt.Errorf("invalid Live client content")
		}
		w.ClientContent = &clientContent{TurnComplete: m.Content.TurnComplete}
		for _, turn := range m.Content.Turns {
			parts, err := encodeParts(turn.Content, d)
			if err != nil {
				return nil, err
			}
			role := "user"
			if turn.Role == canonical.RoleAssistant {
				role = "model"
			}
			w.ClientContent.Turns = append(w.ClientContent.Turns, content{Role: role, Parts: parts})
		}
	case canonical.LiveRealtimeInput:
		if !client || m.Realtime == nil {
			return nil, fmt.Errorf("invalid Live realtime input")
		}
		r := m.Realtime
		w.RealtimeInput = &realtimeInput{Text: r.Text, AudioStreamEnd: r.AudioStreamEnd}
		if r.Audio != nil {
			w.RealtimeInput.Audio = &blob{MIMEType: r.Audio.MIMEType, Data: r.Audio.Data}
		}
		if r.Video != nil {
			w.RealtimeInput.Video = &blob{MIMEType: r.Video.MIMEType, Data: r.Video.Data}
		}
		if r.ActivityStart {
			w.RealtimeInput.ActivityStart = json.RawMessage(`{}`)
		}
		if r.ActivityEnd {
			w.RealtimeInput.ActivityEnd = json.RawMessage(`{}`)
		}
	case canonical.LiveToolResponse:
		if !client {
			return nil, fmt.Errorf("invalid Live tool response")
		}
		w.ToolResponse = &toolResponse{}
		for _, f := range m.ToolResponses {
			w.ToolResponse.FunctionResponses = append(w.ToolResponse.FunctionResponses,
				functionResponse{ID: f.ID, Name: f.Name, Response: f.Response})
		}
	case canonical.LiveSetupComplete:
		if client {
			return nil, fmt.Errorf("invalid Live setup complete")
		}
		w.SetupComplete = json.RawMessage(`{}`)
	case canonical.LiveServerContent:
		if client || m.Output == nil {
			return nil, fmt.Errorf("invalid Live server content")
		}
		o := m.Output
		w.ServerContent = &serverContent{GenerationComplete: o.GenerationComplete,
			TurnComplete: o.TurnComplete, Interrupted: o.Interrupted, InteractionStatus: o.InteractionStatus}
		if o.HasModelTurn {
			parts, err := encodeParts(o.Parts, d)
			if err != nil {
				return nil, err
			}
			w.ServerContent.ModelTurn = &content{Role: o.ModelRole, Parts: parts}
		}
		if o.InputTranscript != "" {
			w.ServerContent.InputTranscription = &transcript{Text: o.InputTranscript, LanguageCode: o.InputLanguage}
		}
		if o.OutputTranscript != "" {
			w.ServerContent.OutputTranscription = &transcript{Text: o.OutputTranscript, LanguageCode: o.OutputLanguage}
		}
	case canonical.LiveToolCall:
		if client {
			return nil, fmt.Errorf("invalid Live tool call")
		}
		w.ToolCall = &toolCall{}
		for _, f := range m.ToolCalls {
			w.ToolCall.FunctionCalls = append(w.ToolCall.FunctionCalls,
				functionCall{ID: f.ID, Name: f.Name, Args: f.Arguments})
		}
	case canonical.LiveToolCancel:
		if client {
			return nil, fmt.Errorf("invalid Live tool cancellation")
		}
		w.ToolCallCancellation = &toolCancel{IDs: m.CancelledToolIDs}
	case canonical.LiveGoAway:
		if client {
			return nil, fmt.Errorf("invalid Live go-away")
		}
		w.GoAway = &goAway{TimeLeft: m.GoAwayTimeLeft}
	case canonical.LiveResume:
		if client {
			return nil, fmt.Errorf("invalid Live resumption update")
		}
		w.SessionResumptionUpdate = &resume{NewHandle: m.ResumeHandle, Resumable: m.Resumable}
	case canonical.LiveUsage:
		if client || m.Usage == nil {
			return nil, fmt.Errorf("invalid Live usage")
		}
	case canonical.LiveNativeEvent:
		if m.NativeEvent == nil || m.NativeEvent.Protocol != string(Name) || !json.Valid(m.NativeEvent.Raw) {
			return nil, fmt.Errorf("provider-bound Live event cannot be sent to Gemini Live")
		}
		return append([]byte(nil), m.NativeEvent.Raw...), nil
	default:
		return nil, fmt.Errorf("unknown Live message kind %q", m.Kind)
	}
	if m.Usage != nil {
		w.UsageMetadata = &usage{PromptTokenCount: m.Usage.InputTokens,
			ResponseTokenCount:      m.Usage.OutputTokens,
			CachedContentTokenCount: m.Usage.CachedInputTokens,
			ThoughtsTokenCount:      m.Usage.ReasoningTokens}
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	return protocol.Merge(Name, m.Extensions, bytes.TrimSpace(b), d), nil
}
