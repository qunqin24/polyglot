package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// Driver adapts a Target to the calling conventions of one protocol family:
// which path a chat request goes to, and how credentials are presented.
// It never touches the request body, which the codec already produced.
type Driver interface {
	Protocol() protocol.Name

	// ChatRequest builds the upstream HTTP request. model is the upstream
	// model name; some protocols need it in the URL.
	ChatRequest(ctx context.Context, t *Target, model string, body []byte, stream bool) (*http.Request, error)
}

// DiscoveredModel is one entry from a provider's model listing.
type DiscoveredModel struct {
	ID          string
	DisplayName string
}

// ModelDiscoverer is an optional capability. A driver that has no reliable way
// to list models simply does not implement it, and Polyglot falls back to
// letting the operator add models by hand — discovery is never a requirement
// for a provider to work.
type ModelDiscoverer interface {
	// ModelsRequest builds a list-models request. ok is false when this
	// particular target cannot be listed.
	ModelsRequest(ctx context.Context, t *Target) (req *http.Request, ok bool)

	// ParseModels extracts the models from a list-models body.
	ParseModels(body []byte) ([]DiscoveredModel, error)
}

// DiscovererFor returns the discovery capability for a protocol, if it has one.
func DiscovererFor(p protocol.Name) (ModelDiscoverer, bool) {
	d, ok := drivers[p]
	if !ok {
		return nil, false
	}
	md, ok := d.(ModelDiscoverer)
	return md, ok
}

var drivers = map[protocol.Name]Driver{
	protocol.OpenAI:             openAIDriver{},
	protocol.Anthropic:          anthropicDriver{},
	protocol.Gemini:             geminiDriver{},
	protocol.OpenAIResponses:    responsesDriver{},
	protocol.GeminiInteractions: interactionsDriver{},
}

func DriverFor(p protocol.Name) (Driver, error) {
	d, ok := drivers[p]
	if !ok {
		return nil, fmt.Errorf("no driver for protocol %q", p)
	}
	return d, nil
}

// applyCustom adds operator-configured headers. It runs last but refuses to
// overwrite the auth header the driver set, so a stray config cannot silently
// break authentication.
func applyCustom(req *http.Request, t *Target) {
	keys := make([]string, 0, len(t.Headers))
	for k := range t.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if req.Header.Get(k) != "" {
			continue
		}
		req.Header.Set(k, t.Headers[k])
	}
}

func newJSONRequest(ctx context.Context, method, urlStr string, body []byte) (*http.Request, error) {
	var r *http.Request
	var err error
	if body == nil {
		r, err = http.NewRequestWithContext(ctx, method, urlStr, nil)
	} else {
		r, err = http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(body))
		if err == nil {
			r.ContentLength = int64(len(body))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept-Encoding", "identity") // keep SSE unbuffered
	r.Header.Set("User-Agent", "Polyglot/0.1")
	return r, nil
}

// --- OpenAI-compatible ----------------------------------------------------

type openAIDriver struct{}

func (openAIDriver) Protocol() protocol.Name { return protocol.OpenAI }

func (openAIDriver) ChatRequest(ctx context.Context, t *Target, _ string, body []byte, stream bool) (*http.Request, error) {
	req, err := newJSONRequest(ctx, http.MethodPost, joinURL(openAIBaseURL(t.BaseURL), "chat/completions"), body)
	if err != nil {
		return nil, err
	}
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyCustom(req, t)
	return req, nil
}

func (openAIDriver) ModelsRequest(ctx context.Context, t *Target) (*http.Request, bool) {
	req, err := newJSONRequest(ctx, http.MethodGet, joinURL(openAIBaseURL(t.BaseURL), "models"), nil)
	if err != nil {
		return nil, false
	}
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	applyCustom(req, t)
	return req, true
}

func (openAIDriver) ParseModels(body []byte) ([]DiscoveredModel, error) {
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"` // OpenRouter and friends add this
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse models list: %w", err)
	}
	if out.Data == nil {
		return nil, fmt.Errorf("model list has no 'data' array")
	}
	models := make([]DiscoveredModel, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, DiscoveredModel{ID: m.ID, DisplayName: m.Name})
		}
	}
	sortModels(models)
	return models, nil
}

// --- OpenAI Responses -----------------------------------------------------

// responsesDriver reaches the same hosts as openAIDriver and authenticates the
// same way; only the path differs. Model discovery is identical, so it embeds
// the OpenAI driver rather than repeating it.
type responsesDriver struct{ openAIDriver }

func (responsesDriver) Protocol() protocol.Name { return protocol.OpenAIResponses }

func (responsesDriver) ChatRequest(ctx context.Context, t *Target, _ string, body []byte, stream bool) (*http.Request, error) {
	req, err := newJSONRequest(ctx, http.MethodPost, joinURL(openAIBaseURL(t.BaseURL), "responses"), body)
	if err != nil {
		return nil, err
	}
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyCustom(req, t)
	return req, nil
}

// openAIBaseURL lets built-in providers be configured with only their host.
// Their compatibility endpoints use different prefixes, so a blanket /v1
// rule would break OpenRouter and Groq. Unknown hosts are intentionally left
// alone: a private OpenAI-compatible server may expose chat/completions at its
// root. Any explicit path also wins over these conveniences.
func openAIBaseURL(base string) string {
	trimmed := strings.TrimRight(base, "/")
	u, err := url.Parse(trimmed)
	if err != nil || strings.Trim(u.EscapedPath(), "/") != "" {
		return trimmed
	}

	var prefix string
	switch strings.ToLower(u.Hostname()) {
	case "api.openai.com", "api.deepseek.com", "api.siliconflow.cn":
		prefix = "/v1"
	case "openrouter.ai":
		prefix = "/api/v1"
	case "api.groq.com":
		prefix = "/openai/v1"
	case "127.0.0.1", "localhost", "::1":
		if u.Port() == "11434" {
			prefix = "/v1"
		}
	}
	if prefix == "" {
		return trimmed
	}
	u.Path = prefix
	u.RawPath = ""
	return u.String()
}

// --- Anthropic ------------------------------------------------------------

// anthropicVersion is the API version header Anthropic requires. Operators can
// override it per provider via custom headers.
const anthropicVersion = "2023-06-01"

type anthropicDriver struct{}

func (anthropicDriver) Protocol() protocol.Name { return protocol.Anthropic }

func (anthropicDriver) ChatRequest(ctx context.Context, t *Target, _ string, body []byte, stream bool) (*http.Request, error) {
	req, err := newJSONRequest(ctx, http.MethodPost, joinURL(ensureSuffix(t.BaseURL, "v1"), "messages"), body)
	if err != nil {
		return nil, err
	}
	if t.APIKey != "" {
		req.Header.Set("x-api-key", t.APIKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyCustom(req, t)
	return req, nil
}

func (anthropicDriver) ModelsRequest(ctx context.Context, t *Target) (*http.Request, bool) {
	req, err := newJSONRequest(ctx, http.MethodGet, joinURL(ensureSuffix(t.BaseURL, "v1"), "models?limit=1000"), nil)
	if err != nil {
		return nil, false
	}
	if t.APIKey != "" {
		req.Header.Set("x-api-key", t.APIKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	applyCustom(req, t)
	return req, true
}

func (anthropicDriver) ParseModels(body []byte) ([]DiscoveredModel, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse models list: %w", err)
	}
	if out.Data == nil {
		return nil, fmt.Errorf("model list has no 'data' array")
	}
	models := make([]DiscoveredModel, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, DiscoveredModel{ID: m.ID, DisplayName: m.DisplayName})
		}
	}
	sortModels(models)
	return models, nil
}

// --- Gemini ---------------------------------------------------------------

type geminiDriver struct{}

func (geminiDriver) Protocol() protocol.Name { return protocol.Gemini }

func (geminiDriver) ChatRequest(ctx context.Context, t *Target, model string, body []byte, stream bool) (*http.Request, error) {
	if model == "" {
		return nil, fmt.Errorf("gemini requires an upstream model name")
	}
	// Gemini puts the model and the method in the path.
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	path := "models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":" + method
	target := joinURL(geminiBaseURL(t.BaseURL), path)
	if stream {
		// alt=sse gives us a real event stream instead of a chunked JSON array.
		target += "?alt=sse"
	}
	req, err := newJSONRequest(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	if t.APIKey != "" {
		req.Header.Set("x-goog-api-key", t.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyCustom(req, t)
	return req, nil
}

func (geminiDriver) ModelsRequest(ctx context.Context, t *Target) (*http.Request, bool) {
	req, err := newJSONRequest(ctx, http.MethodGet, joinURL(geminiBaseURL(t.BaseURL), "models?pageSize=1000"), nil)
	if err != nil {
		return nil, false
	}
	if t.APIKey != "" {
		req.Header.Set("x-goog-api-key", t.APIKey)
	}
	applyCustom(req, t)
	return req, true
}

func (geminiDriver) ParseModels(body []byte) ([]DiscoveredModel, error) {
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		PublisherModels []struct {
			Name string `json:"name"`
		} `json:"publisherModels"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse models list: %w", err)
	}
	if out.Models == nil && out.PublisherModels == nil {
		return nil, fmt.Errorf("model list has neither a 'models' nor a 'publisherModels' array")
	}
	models := make([]DiscoveredModel, 0, len(out.Models))
	for _, m := range out.Models {
		// Gemini lists embedding and other models alongside chat models.
		// Keep only what Polyglot can actually route.
		if len(m.SupportedGenerationMethods) > 0 && !supportsGeneration(m.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		if id != "" {
			models = append(models, DiscoveredModel{ID: id, DisplayName: m.DisplayName})
		}
	}
	for _, m := range out.PublisherModels {
		// Agent Platform's publisher listing covers Model Garden, not only the
		// Gemini generateContent family. This driver can call Google's Gemini
		// models directly; other publisher models need their own serving API.
		const marker = "publishers/google/models/"
		pos := strings.Index(m.Name, marker)
		if pos < 0 {
			continue
		}
		id := m.Name[pos+len(marker):]
		if !strings.HasPrefix(id, "gemini-") {
			continue
		}
		models = append(models, DiscoveredModel{ID: id})
	}
	sortModels(models)
	return models, nil
}

// geminiBaseURL distinguishes the Developer API from Agent Platform. The
// former is rooted at /v1beta; the latter already includes a publisher parent,
// for example /v1/publishers/google or
// /v1/projects/p/locations/global/publishers/google.
func geminiBaseURL(base string) string {
	trimmed := strings.TrimRight(base, "/")
	if strings.Contains(trimmed, "/publishers/") {
		return trimmed
	}
	return ensureSuffix(trimmed, "v1beta")
}

func supportsGeneration(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" || m == "streamGenerateContent" {
			return true
		}
	}
	return false
}

func sortModels(m []DiscoveredModel) {
	sort.Slice(m, func(i, j int) bool { return m[i].ID < m[j].ID })
}

// interactionsDriver calls Google's Interactions API.
//
// It shares Gemini's credential header but nothing else about the URL: the
// model and the streaming choice are fields in the body, so one path serves
// every call and there is no per-method URL to build. It also reuses the
// model listing, which is the same endpoint for both Google protocols.
type interactionsDriver struct{}

func (interactionsDriver) Protocol() protocol.Name { return protocol.GeminiInteractions }

func (interactionsDriver) ChatRequest(ctx context.Context, t *Target, model string, body []byte, stream bool) (*http.Request, error) {
	if model == "" {
		return nil, fmt.Errorf("the interactions API requires an upstream model name")
	}
	target := joinURL(ensureSuffix(t.BaseURL, "v1beta"), "interactions")
	req, err := newJSONRequest(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	if t.APIKey != "" {
		req.Header.Set("x-goog-api-key", t.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyCustom(req, t)
	return req, nil
}

func (interactionsDriver) ModelsRequest(ctx context.Context, t *Target) (*http.Request, bool) {
	return geminiDriver{}.ModelsRequest(ctx, t)
}

func (interactionsDriver) ParseModels(body []byte) ([]DiscoveredModel, error) {
	return geminiDriver{}.ParseModels(body)
}
