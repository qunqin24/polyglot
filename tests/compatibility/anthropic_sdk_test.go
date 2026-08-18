package compatibility

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func anthropicClient(t *testing.T) anthropic.Client {
	t.Helper()
	return anthropic.NewClient(
		option.WithBaseURL(gw.baseURL),
		option.WithAPIKey(gw.apiKey),
		option.WithMaxRetries(0),
	)
}

func TestAnthropicSDKMessages(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Hello from Anthropic.", inputTokens: 14, outputTokens: 6})
	c := anthropicClient(t)

	msg, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 256,
		System: []anthropic.TextBlockParam{
			{Text: "You are terse."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Say hello.")),
		},
	})
	if err != nil {
		t.Fatalf("SDK could not complete the request: %v", err)
	}

	if msg.Role != "assistant" || msg.Type != "message" {
		t.Errorf("envelope = role %q type %q", msg.Role, msg.Type)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "Hello from Anthropic." {
		t.Fatalf("content = %+v", msg.Content)
	}
	if msg.StopReason != anthropic.StopReasonEndTurn {
		t.Errorf("stop_reason = %q", msg.StopReason)
	}
	if msg.Usage.InputTokens != 14 || msg.Usage.OutputTokens != 6 {
		t.Errorf("usage = %+v", msg.Usage)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "You are terse.") {
		t.Errorf("system prompt did not reach the upstream: %s", body)
	}
}

func TestAnthropicSDKStreaming(t *testing.T) {
	gw.upstream.use(t, scenario{text: "Anthropic streaming works.", inputTokens: 14, outputTokens: 8})
	c := anthropicClient(t)

	stream := c.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Stream something.")),
		},
	})
	defer stream.Close()

	// Accumulate returns a fully-formed Message only if the whole event
	// sequence was valid: message_start, block lifecycles, message_delta,
	// message_stop, in order.
	var msg anthropic.Message
	events := 0
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			t.Fatalf("SDK rejected event %d: %v", events, err)
		}
		events++
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if events < 5 {
		t.Errorf("expected a full event sequence, got %d event(s)", events)
	}
	if len(msg.Content) == 0 || msg.Content[0].Text != "Anthropic streaming works." {
		t.Fatalf("accumulated content = %+v", msg.Content)
	}
	if msg.StopReason != anthropic.StopReasonEndTurn {
		t.Errorf("stop_reason = %q", msg.StopReason)
	}
	if msg.Usage.OutputTokens != 8 {
		t.Errorf("usage = %+v", msg.Usage)
	}
}

func TestAnthropicSDKToolUseAndResult(t *testing.T) {
	gw.upstream.use(t, scenario{toolName: "get_weather", toolArgs: `{"city":"Paris"}`})
	c := anthropicClient(t)

	tools := []anthropic.ToolUnionParam{{
		OfTool: &anthropic.ToolParam{
			Name:        "get_weather",
			Description: anthropic.String("Look up the weather"),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{"city": map[string]any{"type": "string"}},
				Required:   []string{"city"},
			},
		},
	}}

	msg, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Weather in Paris?")),
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("tool use request failed: %v", err)
	}
	if msg.StopReason != anthropic.StopReasonToolUse {
		t.Errorf("stop_reason = %q", msg.StopReason)
	}

	var useID string
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			useID = block.ID
			if block.Name != "get_weather" {
				t.Errorf("tool name = %q", block.Name)
			}
			if !strings.Contains(string(block.Input), "Paris") {
				t.Errorf("tool input = %s", block.Input)
			}
		}
	}
	if useID == "" {
		t.Fatalf("no tool_use block: %+v", msg.Content)
	}

	// Second turn: replay the assistant turn through the SDK's own converter
	// and answer the tool call.
	gw.upstream.use(t, scenario{text: "It is 18C in Paris."})
	second, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Weather in Paris?")),
			msg.ToParam(),
			anthropic.NewUserMessage(anthropic.NewToolResultBlock(useID, "18C and sunny", false)),
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("tool result turn failed: %v", err)
	}
	if second.Content[0].Text != "It is 18C in Paris." {
		t.Errorf("second turn content = %+v", second.Content)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "18C and sunny") {
		t.Errorf("the tool result never reached the upstream: %s", body)
	}
}

func TestAnthropicSDKThinking(t *testing.T) {
	gw.upstream.use(t, scenario{reasoning: "Weighing the options.", text: "42"})
	c := anthropicClient(t)

	msg, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Think, then answer.")),
		},
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: 1024},
		},
	})
	if err != nil {
		t.Fatalf("thinking request failed: %v", err)
	}

	var sawThinking, sawText bool
	for _, block := range msg.Content {
		switch block.Type {
		case "thinking":
			sawThinking = true
			if block.Thinking != "Weighing the options." {
				t.Errorf("thinking = %q", block.Thinking)
			}
			// A thinking block without a signature cannot be replayed.
			if block.Signature == "" {
				t.Errorf("thinking block reached the client with no signature")
			}
		case "text":
			sawText = true
		}
	}
	if !sawThinking || !sawText {
		t.Fatalf("blocks = %+v", msg.Content)
	}

	_, body := gw.upstream.lastRequest()
	if !strings.Contains(string(body), "thinking") {
		t.Errorf("the thinking config did not reach the upstream: %s", body)
	}
}

func TestAnthropicSDKErrorShape(t *testing.T) {
	gw.upstream.use(t, scenario{status: 400, errMsg: "bad request from upstream"})
	c := anthropicClient(t)

	_, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 16,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("SDK did not recognise the error body as an API error: %v", err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "bad request from upstream") {
		t.Errorf("message lost: %v", apiErr)
	}
}

func TestAnthropicSDKClientCancel(t *testing.T) {
	gw.upstream.use(t, scenario{text: "never delivered", delay: 5 * time.Second})
	c := anthropicClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     upAnthropic + "::mock-model",
		MaxTokens: 16,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
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

// TestAnthropicSDKCrossProtocol runs the real Claude SDK against upstreams that
// are not Anthropic at all.
func TestAnthropicSDKCrossProtocol(t *testing.T) {
	for _, up := range []string{upOpenAI, upGemini, upResponses} {
		t.Run(up, func(t *testing.T) {
			gw.upstream.use(t, scenario{text: "Converted reply."})
			c := anthropicClient(t)

			msg, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
				Model:     up + "::mock-model",
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: "Be terse."}},
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock("Hello.")),
				},
			})
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if len(msg.Content) == 0 || msg.Content[0].Text != "Converted reply." {
				t.Errorf("content = %+v", msg.Content)
			}
			if msg.Type != "message" {
				t.Errorf("the client must still see an Anthropic envelope, got type = %q", msg.Type)
			}
		})
	}
}
