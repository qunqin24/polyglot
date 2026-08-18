package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An OpenAI client calling an Anthropic upstream that served most of the
// prompt from its cache.
//
// This is the pairing where the two vendors' accounting disagrees, and the one
// worth proving over a real HTTP path rather than in a codec test. Anthropic's
// input_tokens counts only what the cache did not serve; OpenAI's
// prompt_tokens counts the whole prompt with the cached part named inside it.
// Copying the number across produced two failures at once — a reply telling an
// OpenAI client that 5000 of its 40 prompt tokens were cached, and a usage
// total that under-counted every cached request by the part the cache served.
func TestACachedAnthropicPromptIsCountedWholeForAnOpenAIClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude",
			"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":40,"output_tokens":12,
			         "cache_read_input_tokens":5000,"cache_creation_input_tokens":300}
		}`))
	}, "anthropic")

	// 40 fresh + 5000 read from cache + 300 written to it.
	const wantPrompt = 5340

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out struct {
		Usage struct {
			PromptTokens  int `json:"prompt_tokens"`
			PromptDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Usage.PromptTokens != wantPrompt {
		t.Errorf("prompt_tokens = %d, want %d", out.Usage.PromptTokens, wantPrompt)
	}
	if out.Usage.PromptDetails.CachedTokens != 5000 {
		t.Errorf("cached_tokens = %d, want 5000", out.Usage.PromptDetails.CachedTokens)
	}
	// The invariant an OpenAI client relies on when it works out what it owes.
	if out.Usage.PromptDetails.CachedTokens > out.Usage.PromptTokens {
		t.Errorf("cached_tokens (%d) exceeds prompt_tokens (%d), which cannot happen in OpenAI's accounting",
			out.Usage.PromptDetails.CachedTokens, out.Usage.PromptTokens)
	}

	log := h.waitForLog(t)
	if log.InputTokens != wantPrompt {
		t.Errorf("logged input tokens = %d, want %d", log.InputTokens, wantPrompt)
	}
	if log.CachedInputTokens != 5000 || log.CacheWriteTokens != 300 {
		t.Errorf("logged cache split = %d read / %d written, want 5000 / 300",
			log.CachedInputTokens, log.CacheWriteTokens)
	}
	// What the /models hit rate divides. Both parts of the prompt cache are
	// inside the input total, never added on top of it.
	if log.CachedInputTokens > log.InputTokens {
		t.Errorf("cached (%d) exceeds the input total it is part of (%d)",
			log.CachedInputTokens, log.InputTokens)
	}
}

// Anthropic on both sides: the three counters must come back apart exactly as
// they went in, or a round trip through Polyglot would change the bill.
func TestAnthropicCacheCountersSurviveARoundTrip(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude",
			"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":40,"output_tokens":12,
			         "cache_read_input_tokens":5000,"cache_creation_input_tokens":300}
		}`))
	}, "anthropic")

	resp := h.post("/v1/messages",
		`{"model":"my-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out struct {
		Usage struct {
			InputTokens   int `json:"input_tokens"`
			OutputTokens  int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Usage.InputTokens != 40 || out.Usage.CacheRead != 5000 || out.Usage.CacheCreation != 300 {
		t.Errorf("usage = %+v, want the upstream's own 40 / 5000 / 300", out.Usage)
	}
}
