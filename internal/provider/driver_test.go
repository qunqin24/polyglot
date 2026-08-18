package provider

import (
	"context"
	"strings"
	"testing"
)

func TestOpenAIDriverCompletesBuiltInProviderHosts(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.openai.com", "https://api.openai.com/v1/chat/completions"},
		{"https://openrouter.ai", "https://openrouter.ai/api/v1/chat/completions"},
		{"https://api.deepseek.com", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.siliconflow.cn", "https://api.siliconflow.cn/v1/chat/completions"},
		{"https://api.groq.com", "https://api.groq.com/openai/v1/chat/completions"},
		{"http://127.0.0.1:11434", "http://127.0.0.1:11434/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			req, err := (openAIDriver{}).ChatRequest(context.Background(), &Target{BaseURL: tc.base}, "m", []byte(`{}`), false)
			if err != nil {
				t.Fatal(err)
			}
			if got := req.URL.String(); got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenAIDriverPreservesExplicitAndUnknownBases(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.openai.com/proxy/v2", "https://api.openai.com/proxy/v2/chat/completions"},
		{"https://llm.internal.example", "https://llm.internal.example/chat/completions"},
	}
	for _, tc := range cases {
		req, err := (openAIDriver{}).ChatRequest(context.Background(), &Target{BaseURL: tc.base}, "m", []byte(`{}`), false)
		if err != nil {
			t.Fatal(err)
		}
		if got := req.URL.String(); got != tc.want {
			t.Fatalf("URL = %q, want %q", got, tc.want)
		}
	}
}

func TestResponsesDriverCompletesOpenAIHost(t *testing.T) {
	req, err := (responsesDriver{}).ChatRequest(context.Background(), &Target{BaseURL: "https://api.openai.com"}, "m", []byte(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.String(), "https://api.openai.com/v1/responses"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestGeminiDriverAgentPlatformExpressURL(t *testing.T) {
	target := &Target{
		BaseURL: "https://aiplatform.googleapis.com/v1/publishers/google",
		APIKey:  "test-key",
	}
	req, err := (geminiDriver{}).ChatRequest(context.Background(), target, "gemini-3.5-flash", []byte(`{}`), true)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-3.5-flash:streamGenerateContent?alt=sse"
	if req.URL.String() != want {
		t.Fatalf("URL = %q, want %q", req.URL, want)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "test-key" {
		t.Errorf("x-goog-api-key = %q", got)
	}
	if _, ok := (geminiDriver{}).ModelsRequest(context.Background(), target); ok {
		t.Fatal("Agent Platform model listing was reported supported")
	}
}

func TestGeminiDriverAgentPlatformProjectURL(t *testing.T) {
	target := &Target{BaseURL: "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google"}
	req, err := (geminiDriver{}).ChatRequest(context.Background(), target, "gemini-3.5-pro", []byte(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-3.5-pro:generateContent"
	if req.URL.String() != want {
		t.Fatalf("URL = %q, want %q", req.URL, want)
	}
	if _, ok := (geminiDriver{}).ModelsRequest(context.Background(), target); ok {
		t.Fatal("Agent Platform model listing was reported supported")
	}
}

func TestGeminiDriverDeveloperAPIStillAddsVersion(t *testing.T) {
	target := &Target{BaseURL: "https://generativelanguage.googleapis.com"}
	req, err := (geminiDriver{}).ChatRequest(context.Background(), target, "gemini-2.5-flash", []byte(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"
	if req.URL.String() != want {
		t.Fatalf("URL = %q, want %q", req.URL, want)
	}
}

func TestGeminiDriverParsesAgentPlatformPublisherModels(t *testing.T) {
	body := `{"publisherModels":[
		{"name":"publishers/google/models/gemini-3.5-flash"},
		{"name":"projects/p/locations/global/publishers/google/models/gemini-3.5-pro"},
		{"name":"publishers/google/models/text-embedding-005"},
		{"name":"publishers/meta/models/llama"}
	]}`
	models, err := (geminiDriver{}).ParseModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if got := strings.Join(ids, ","); got != "gemini-3.5-flash,gemini-3.5-pro" {
		t.Fatalf("models = %q", got)
	}
}
