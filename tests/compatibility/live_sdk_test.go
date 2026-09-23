package compatibility

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestGeminiLiveSDKWebSocketSession(t *testing.T) {
	// The Go SDK forces wss for http base URLs, so a local HTTP fixture uses
	// ws explicitly. Production uses wss for the configured HTTPS origin.
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: gw.apiKey, Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: strings.Replace(gw.baseURL, "http://", "ws://", 1), APIVersion: "v1beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := c.Live.Connect(ctx, upGemini+"::mock-model", &genai.LiveConnectConfig{
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		Tools:                    []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", Description: "Look up the weather"}}}},
	})
	if err != nil {
		t.Fatalf("official SDK Live.Connect: %v", err)
	}
	defer session.Close()
	if session.SetupComplete == nil {
		t.Fatal("missing setupComplete")
	}
	if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
		Audio: &genai.Blob{MIMEType: "audio/pcm;rate=16000", Data: []byte{0, 1, 2, 3}},
	}); err != nil {
		t.Fatalf("send audio: %v", err)
	}
	if err := session.SendClientContent(genai.LiveClientContentInput{
		Turns: userText("Weather in Paris?"),
	}); err != nil {
		t.Fatalf("send content: %v", err)
	}
	var call *genai.FunctionCall
	for i := 0; i < 3 && call == nil; i++ {
		msg, err := session.Receive()
		if err != nil {
			t.Fatalf("receive tool call: %v", err)
		}
		if msg.ToolCall != nil && len(msg.ToolCall.FunctionCalls) > 0 {
			call = msg.ToolCall.FunctionCalls[0]
		}
	}
	if call == nil || call.ID != "call-1" || call.Name != "lookup" {
		t.Fatalf("tool call = %+v", call)
	}
	if err := session.SendToolResponse(genai.LiveToolResponseInput{
		FunctionResponses: []*genai.FunctionResponse{{ID: call.ID, Name: call.Name,
			Response: map[string]any{"weather": "sunny"}}},
	}); err != nil {
		t.Fatalf("send tool reply: %v", err)
	}
	var output *genai.LiveServerContent
	for i := 0; i < 3 && output == nil; i++ {
		msg, err := session.Receive()
		if err != nil {
			t.Fatalf("receive audio: %v", err)
		}
		output = msg.ServerContent
	}
	if output == nil || output.ModelTurn == nil || len(output.ModelTurn.Parts) != 1 ||
		output.ModelTurn.Parts[0].InlineData == nil ||
		string(output.ModelTurn.Parts[0].InlineData.Data) != string([]byte{0, 1, 2, 3}) ||
		output.OutputTranscription == nil || output.OutputTranscription.Text != "Paris is sunny." || !output.TurnComplete {
		t.Fatalf("Live output = %+v", output)
	}
	gw.upstream.mu.Lock()
	frames := append([]json.RawMessage(nil), gw.upstream.liveFrames...)
	path := gw.upstream.lastPath
	auth := gw.upstream.lastAuth.Get("x-goog-api-key")
	gw.upstream.mu.Unlock()
	if !strings.HasSuffix(path, ".GenerativeService.BidiGenerateContent") || auth != "sk-mock-upstream" {
		t.Fatalf("upstream path/auth = %s / %s", path, auth)
	}
	if len(frames) != 4 || !strings.Contains(string(frames[0]), "models/mock-model") ||
		!strings.Contains(string(frames[0]), "functionDeclarations") ||
		!strings.Contains(string(frames[1]), "audio/pcm;rate=16000") ||
		!strings.Contains(string(frames[3]), "sunny") {
		t.Fatalf("upstream Live frames = %s", frames)
	}
}
