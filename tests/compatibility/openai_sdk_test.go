package compatibility

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func openaiClient(t *testing.T) openai.Client {
	t.Helper()
	return openai.NewClient(
		option.WithBaseURL(gw.baseURL+"/v1"),
		option.WithAPIKey(gw.apiKey),
		option.WithMaxRetries(0),
	)
}

// --- Chat Completions ------------------------------------------------------

func TestOpenAISDKChatCompletion(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Hello from the upstream.", inputTokens: 11, outputTokens: 5})
	c := openaiClient(t)

	resp, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("Be terse."),
			openai.UserMessage("Say hello."),
		},
	})
	if err != nil {
		t.Fatalf("SDK could not complete the request: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d", len(resp.Choices))
	}
	if got := resp.Choices[0].Message.Content; got != "Hello from the upstream." {
		t.Errorf("content = %q", got)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("object = %q", resp.Object)
	}

	// The system message must have reached the upstream as a system message.
	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), `"system"`) {
		t.Errorf("system message did not reach the upstream: %s", body)
	}
}

func TestOpenAISDKChatStreaming(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Streaming works end to end.", inputTokens: 20, outputTokens: 9})
	c := openaiClient(t)

	stream := c.Chat.Completions.NewStreaming(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Stream something.")},
		// Usage in a stream is opt-in for OpenAI, so it has to be asked for.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	})
	defer stream.Close()

	// The SDK's accumulator is the real test: it only produces a coherent
	// message if every chunk was well-formed and correctly ordered.
	acc := openai.ChatCompletionAccumulator{}
	chunks := 0
	for stream.Next() {
		acc.AddChunk(stream.Current())
		chunks++
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if chunks < 2 {
		t.Errorf("expected an incremental stream, got %d chunk(s)", chunks)
	}
	if len(acc.Choices) == 0 || acc.Choices[0].Message.Content != "Streaming works end to end." {
		t.Fatalf("accumulated message = %+v", acc.Choices)
	}
	if acc.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", acc.Choices[0].FinishReason)
	}
	if acc.Usage.CompletionTokens != 9 {
		t.Errorf("usage did not arrive in the stream: %+v", acc.Usage)
	}
}

// TestOpenAISDKStreamUsageIsOptIn is the other half of the contract: OpenAI
// only sends a usage chunk when stream_options.include_usage was set, and a
// client that did not ask must not receive one.
func TestOpenAISDKStreamUsageIsOptIn(t *testing.T) {
	gw.upstream.use(t, scenario{text: "No usage please.", outputTokens: 9})
	c := openaiClient(t)

	stream := c.Chat.Completions.NewStreaming(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Stream something.")},
	})
	defer stream.Close()

	acc := openai.ChatCompletionAccumulator{}
	for stream.Next() {
		acc.AddChunk(stream.Current())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if acc.Choices[0].Message.Content != "No usage please." {
		t.Errorf("content = %q", acc.Choices[0].Message.Content)
	}
	if acc.Usage.CompletionTokens != 0 {
		t.Errorf("usage was sent without stream_options.include_usage: %+v", acc.Usage)
	}
}

func TestOpenAISDKToolCallingAndToolResult(t *testing.T) {
	gw.upstream.use(t, scenario{toolName: "get_weather", toolArgs: `{"city":"Paris"}`})
	c := openaiClient(t)

	tools := []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        "get_weather",
			Description: openai.String("Look up the weather"),
			Parameters: shared.FunctionParameters{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []string{"city"},
			},
		}),
	}

	resp, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Weather in Paris?")},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("tool call request failed: %v", err)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "get_weather" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("arguments = %q", calls[0].Function.Arguments)
	}

	// Second turn: hand the result back exactly as an agent loop would. The
	// assistant message is replayed from the SDK's own response type, which is
	// where field-shape mismatches usually surface.
	gw.upstream.use(t, scenario{text: "It is 18C in Paris."})
	second, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Weather in Paris?"),
			resp.Choices[0].Message.ToParam(),
			openai.ToolMessage("18C and sunny", calls[0].ID),
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("tool result turn failed: %v", err)
	}
	if second.Choices[0].Message.Content != "It is 18C in Paris." {
		t.Errorf("second turn content = %q", second.Choices[0].Message.Content)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "18C and sunny") {
		t.Errorf("the tool result never reached the upstream: %s", body)
	}
}

func TestOpenAISDKStructuredOutput(t *testing.T) {
	gw.upstream.use(t, scenario{text: `{"city":"Paris","tempC":18}`})
	c := openaiClient(t)

	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"city": map[string]any{"type": "string"}},
		"required":             []string{"city"},
		"additionalProperties": false,
	}
	resp, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Paris weather as JSON.")},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name: "weather", Schema: schema, Strict: openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("structured output request failed: %v", err)
	}
	if !strings.Contains(resp.Choices[0].Message.Content, `"city"`) {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "json_schema") {
		t.Errorf("the schema did not reach the upstream: %s", body)
	}
}

func TestOpenAISDKReasoningContent(t *testing.T) {
	gw.upstream.use(t, scenario{reasoning: "Considering the question.", text: "42"})
	c := openaiClient(t)

	resp, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Think, then answer.")},
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.Choices[0].Message.Content != "42" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
	// reasoning_content is a de-facto extension, so it is read off the raw
	// JSON rather than a typed field.
	if !strings.Contains(resp.RawJSON(), "Considering the question.") {
		t.Errorf("reasoning did not survive to the client: %s", resp.RawJSON())
	}
}

func TestOpenAISDKErrorShape(t *testing.T) {
	gw.upstream.use(t, scenario{status: 429, errMsg: "slow down"})
	c := openaiClient(t)

	_, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	// The SDK must recognise this as a typed API error, not a parse failure.
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("SDK did not recognise the error body as an API error: %v", err)
	}
	if apiErr.StatusCode != 429 {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "slow down") {
		t.Errorf("message lost: %v", apiErr)
	}
}

func TestOpenAISDKClientCancel(t *testing.T) {
	gw.upstream.use(t, scenario{text: "never delivered", delay: 5 * time.Second})
	c := openaiClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    upOpenAI + "::mock-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected cancellation to surface as an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want a context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancellation took %v; the upstream was not torn down promptly", elapsed)
	}
}

// --- Responses -------------------------------------------------------------

func TestOpenAISDKResponses(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Responses reply.", inputTokens: 8, outputTokens: 4})
	c := openaiClient(t)

	resp, err := c.Responses.New(context.Background(), responses.ResponseNewParams{
		Model:        upResponses + "::mock-model",
		Instructions: openai.String("Be terse."),
		Input:        responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("Say hello.")},
	})
	if err != nil {
		t.Fatalf("Responses request failed: %v", err)
	}
	if got := resp.OutputText(); got != "Responses reply." {
		t.Errorf("output text = %q", got)
	}
	if resp.Status != responses.ResponseStatusCompleted {
		t.Errorf("status = %q", resp.Status)
	}
	if resp.Usage.InputTokens != 8 || resp.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "Be terse.") {
		t.Errorf("instructions did not reach the upstream: %s", body)
	}
}

func TestOpenAISDKResponsesStreaming(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Responses streaming works.", inputTokens: 12, outputTokens: 7})
	c := openaiClient(t)

	stream := c.Responses.NewStreaming(context.Background(), responses.ResponseNewParams{
		Model: upResponses + "::mock-model",
		Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("Stream it.")},
	})
	defer stream.Close()

	var text strings.Builder
	var completed *responses.Response
	events := 0
	for stream.Next() {
		ev := stream.Current()
		events++
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.completed":
			r := ev.Response
			completed = &r
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if events < 3 {
		t.Errorf("expected a typed event sequence, got %d event(s)", events)
	}
	if text.String() != "Responses streaming works." {
		t.Errorf("streamed text = %q", text.String())
	}
	if completed == nil {
		t.Fatal("no response.completed event; the SDK cannot finish the stream")
	}
	if completed.Usage.OutputTokens != 7 {
		t.Errorf("usage in response.completed = %+v", completed.Usage)
	}
}

func TestOpenAISDKResponsesToolCall(t *testing.T) {
	gw.upstream.use(t, scenario{toolName: "lookup", toolArgs: `{"q":"paris"}`})
	c := openaiClient(t)

	resp, err := c.Responses.New(context.Background(), responses.ResponseNewParams{
		Model: upResponses + "::mock-model",
		Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("Look it up.")},
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:       "lookup",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
				Strict:     openai.Bool(false),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Responses tool call failed: %v", err)
	}

	var found bool
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			found = true
			if item.Name != "lookup" || item.Arguments.OfString != `{"q":"paris"}` {
				t.Errorf("function_call item = %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("no function_call item in output: %+v", resp.Output)
	}
}

// TestOpenAISDKCrossProtocol is the reason Polyglot exists: the OpenAI SDK
// talking to an Anthropic and a Gemini upstream without knowing it.
func TestOpenAISDKCrossProtocol(t *testing.T) {
	for _, up := range []string{upAnthropic, upGemini} {
		t.Run(up, func(t *testing.T) {
			gw.upstream.use(t, scenario{text: "Converted reply."})
			c := openaiClient(t)

			resp, err := c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: up + "::mock-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.SystemMessage("Be terse."),
					openai.UserMessage("Hello."),
				},
			})
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if resp.Choices[0].Message.Content != "Converted reply." {
				t.Errorf("content = %q", resp.Choices[0].Message.Content)
			}
			if resp.Object != "chat.completion" {
				t.Errorf("the client must still see an OpenAI envelope, got object = %q", resp.Object)
			}
		})
	}
}
