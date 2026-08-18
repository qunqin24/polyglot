package compatibility

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func geminiClient(t *testing.T) *genai.Client {
	t.Helper()
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      gw.apiKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: gw.baseURL},
	})
	if err != nil {
		t.Fatalf("construct genai client: %v", err)
	}
	return c
}

func userText(s string) []*genai.Content {
	return []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: s}}}}
}

func TestGeminiSDKGenerateContent(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Hello from Gemini.", inputTokens: 17, outputTokens: 5})
	c := geminiClient(t)

	resp, err := c.Models.GenerateContent(context.Background(),
		upGemini+"::mock-model", userText("Say hello."),
		&genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "Be terse."}}},
		})
	if err != nil {
		t.Fatalf("SDK could not complete the request: %v", err)
	}

	if got := resp.Text(); got != "Hello from Gemini." {
		t.Errorf("text = %q", got)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("candidates = %d", len(resp.Candidates))
	}
	if resp.Candidates[0].FinishReason != genai.FinishReasonStop {
		t.Errorf("finishReason = %q", resp.Candidates[0].FinishReason)
	}
	if resp.UsageMetadata == nil || resp.UsageMetadata.PromptTokenCount != 17 {
		t.Errorf("usageMetadata = %+v", resp.UsageMetadata)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "Be terse.") {
		t.Errorf("system instruction did not reach the upstream: %s", body)
	}
}

func TestGeminiSDKStreamGenerateContent(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Gemini streaming works.", inputTokens: 17, outputTokens: 7})
	c := geminiClient(t)

	var text strings.Builder
	chunks := 0
	var finish genai.FinishReason
	for resp, err := range c.Models.GenerateContentStream(context.Background(),
		upGemini+"::mock-model", userText("Stream something."), nil) {
		if err != nil {
			t.Fatalf("stream failed after %d chunk(s): %v", chunks, err)
		}
		chunks++
		text.WriteString(resp.Text())
		if len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason != "" {
			finish = resp.Candidates[0].FinishReason
		}
	}
	if chunks < 2 {
		t.Errorf("expected an incremental stream, got %d chunk(s)", chunks)
	}
	if text.String() != "Gemini streaming works." {
		t.Errorf("streamed text = %q", text.String())
	}
	if finish != genai.FinishReasonStop {
		t.Errorf("finishReason = %q", finish)
	}
}

func TestGeminiSDKFunctionCallingAndResponse(t *testing.T) {
	gw.upstream.use(t, scenario{toolName: "get_weather", toolArgs: `{"city":"Paris"}`})
	c := geminiClient(t)

	tools := []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name:        "get_weather",
			Description: "Look up the weather",
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: map[string]*genai.Schema{"city": {Type: genai.TypeString}},
				Required:   []string{"city"},
			},
		}},
	}}

	resp, err := c.Models.GenerateContent(context.Background(),
		upGemini+"::mock-model", userText("Weather in Paris?"),
		&genai.GenerateContentConfig{Tools: tools})
	if err != nil {
		t.Fatalf("function calling request failed: %v", err)
	}

	calls := resp.FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("functionCalls = %+v", calls)
	}
	if calls[0].Args["city"] != "Paris" {
		t.Errorf("args = %+v", calls[0].Args)
	}

	// Second turn: replay the model turn and answer with a functionResponse,
	// which is what Gemini 3 validates thought signatures on.
	gw.upstream.use(t, scenario{text: "It is 18C in Paris."})
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "Weather in Paris?"}}},
		resp.Candidates[0].Content,
		{Role: "user", Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     "get_weather",
				Response: map[string]any{"result": "18C and sunny"},
			},
		}}},
	}
	second, err := c.Models.GenerateContent(context.Background(),
		upGemini+"::mock-model", history, &genai.GenerateContentConfig{Tools: tools})
	if err != nil {
		t.Fatalf("function response turn failed: %v", err)
	}
	if second.Text() != "It is 18C in Paris." {
		t.Errorf("second turn text = %q", second.Text())
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "18C and sunny") {
		t.Errorf("the function response never reached the upstream: %s", body)
	}
	// Gemini 3 rejects a replayed function call whose thought signature was
	// lost. The mock issues one, so it must come back.
	if !strings.Contains(string(body), "thoughtSignature") {
		t.Errorf("the thought signature was lost on the replay: %s", body)
	}
}

func TestGeminiSDKErrorShape(t *testing.T) {
	gw.upstream.use(t, scenario{status: 400, errMsg: "bad request from upstream"})
	c := geminiClient(t)

	_, err := c.Models.GenerateContent(context.Background(),
		upGemini+"::mock-model", userText("hi"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("SDK did not recognise the error body as an API error: %v", err)
	}
	if apiErr.Code != 400 {
		t.Errorf("code = %d", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "bad request from upstream") {
		t.Errorf("message lost: %+v", apiErr)
	}
}

func TestGeminiSDKClientCancel(t *testing.T) {
	gw.upstream.use(t, scenario{text: "never delivered", delay: 5 * time.Second})
	c := geminiClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Models.GenerateContent(ctx, upGemini+"::mock-model", userText("hi"), nil)
	if err == nil {
		t.Fatal("expected cancellation to surface as an error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancellation took %v; the upstream was not torn down promptly", elapsed)
	}
}

// TestGeminiSDKCrossProtocol runs Google's SDK against upstreams that are not
// Google.
func TestGeminiSDKCrossProtocol(t *testing.T) {
	for _, up := range []string{upOpenAI, upAnthropic, upResponses} {
		t.Run(up, func(t *testing.T) {
			gw.upstream.use(t, scenario{text: "Converted reply."})
			c := geminiClient(t)

			resp, err := c.Models.GenerateContent(context.Background(),
				up+"::mock-model", userText("Hello."),
				&genai.GenerateContentConfig{
					SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "Be terse."}}},
				})
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if resp.Text() != "Converted reply." {
				t.Errorf("text = %q", resp.Text())
			}
			if len(resp.Candidates) == 0 || resp.Candidates[0].Content.Role != "model" {
				t.Errorf("the client must still see a Gemini envelope: %+v", resp.Candidates)
			}
		})
	}
}
