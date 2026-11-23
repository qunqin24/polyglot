package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// A Vendor is a built-in upstream that serves one account over several wire
// protocols and decides per model which one is native. The operator picks it
// once and pastes one API key; Polyglot knows each protocol's base URL, how to
// authenticate, and which protocol each model should use.
//
// An ordinary provider is the opposite: one base URL, one protocol, set by the
// operator. Nothing about those changes — a provider with no vendor never
// reaches this file.
type Vendor struct {
	ID    string
	Label string
	// Root is what the WebUI shows as the provider's address. Requests never
	// go to it directly; they go to Bases.
	Root string
	// Primary is the protocol stored on the provider row, and the one used for
	// a model that has no protocol of its own and matches no rule.
	Primary protocol.Name
	// Bases is the base URL for each protocol the vendor serves, in the form
	// the matching driver expects.
	Bases map[protocol.Name]string
	// Bearer makes the Anthropic and Gemini drivers authenticate with
	// "Authorization: Bearer" as well as their own header. Both vendors
	// require it on every endpoint.
	Bearer bool
	// ModelsURL lists models. The listing is protocol-independent, so one
	// request covers every protocol.
	ModelsURL string
	// parse reads a listing and says which protocol each model uses.
	parse func(body []byte) ([]DiscoveredModel, error)
	// fallback picks a protocol from the model id alone, for a model the
	// listing could not classify (added by hand, or the listing was down).
	fallback func(modelID string) protocol.Name
	// session adds a header the vendor requires on every request.
	session func(providerID int64, apiKey string) (name, value string)
}

const (
	VendorOpenCodeGo  = "opencode-go"
	VendorCommandCode = "commandcode"
)

var vendors = map[string]*Vendor{
	VendorOpenCodeGo: {
		ID:      VendorOpenCodeGo,
		Label:   "OpenCode Go",
		Root:    "https://opencode.ai/inference/go",
		Primary: protocol.OpenAI,
		Bases: map[protocol.Name]string{
			protocol.OpenAI:          "https://opencode.ai/inference/go/openai/v1",
			protocol.OpenAIResponses: "https://opencode.ai/inference/go/openai/v1",
			protocol.Anthropic:       "https://opencode.ai/inference/go/anthropic",
			protocol.Gemini:          "https://opencode.ai/inference/go/google",
		},
		Bearer:    true,
		ModelsURL: "https://opencode.ai/inference/go/openai/v1/models",
		parse:     parseOpenCodeModels,
		fallback:  openCodeFallback,
		// Go requires a stable session id on every request ("Go requires it",
		// opencode.ai/docs/console/harnesses). It only has to be stable and
		// well formed, so each provider derives one from its own id and key
		// instead of asking the operator for it.
		session: func(providerID int64, apiKey string) (string, string) {
			sum := sha256.Sum256([]byte(fmt.Sprintf("polyglot:opencode-go:%d:%s", providerID, apiKey)))
			return "x-opencode-session", "ses_" + hex.EncodeToString(sum[:])[:26]
		},
	},
	VendorCommandCode: {
		ID:      VendorCommandCode,
		Label:   "Command Code",
		Root:    "https://api.commandcode.ai/provider",
		Primary: protocol.OpenAI,
		Bases: map[protocol.Name]string{
			protocol.OpenAI:          "https://api.commandcode.ai/provider/v1",
			protocol.OpenAIResponses: "https://api.commandcode.ai/provider/v1",
			protocol.Anthropic:       "https://api.commandcode.ai/provider",
		},
		Bearer:    true,
		ModelsURL: "https://api.commandcode.ai/provider/v1/models",
		parse:     parseCommandCodeModels,
		fallback:  commandCodeFallback,
	},
}

// VendorFor looks up a built-in vendor. An empty id is "no vendor", not an
// error: it is every ordinary provider.
func VendorFor(id string) (*Vendor, bool) {
	v, ok := vendors[id]
	return v, ok
}

// VendorIDs lists the built-in vendors, for validation messages.
func VendorIDs() []string {
	return []string{VendorOpenCodeGo, VendorCommandCode}
}

// ProtocolFor is the protocol a model uses: its own if it has one, otherwise
// the vendor's rule for its id, otherwise the vendor's primary protocol. A
// protocol the vendor does not serve is never returned.
func (v *Vendor) ProtocolFor(override protocol.Name, modelID string) protocol.Name {
	if override != "" {
		if _, ok := v.Bases[override]; ok {
			return override
		}
	}
	if p := v.fallback(modelID); p != "" {
		if _, ok := v.Bases[p]; ok {
			return p
		}
	}
	return v.Primary
}

// Serves reports whether the vendor has an endpoint for a protocol.
func (v *Vendor) Serves(p protocol.Name) bool {
	_, ok := v.Bases[p]
	return ok
}

// Apply points a target at this vendor's endpoint for proto and attaches what
// the vendor needs on every call. It changes the target in place and copies
// Headers first, because the caller's map belongs to a stored provider.
func (v *Vendor) Apply(t *Target, proto protocol.Name, providerID int64) {
	t.Vendor = v
	t.Protocol = proto
	t.BaseURL = v.Bases[proto]
	t.BearerAuth = v.Bearer
	headers := make(map[string]string, len(t.Headers)+1)
	for k, val := range t.Headers {
		headers[k] = val
	}
	if v.session != nil && t.APIKey != "" {
		name, value := v.session(providerID, t.APIKey)
		if _, set := headers[name]; !set {
			headers[name] = value
		}
	}
	t.Headers = headers
}

// ModelsRequest lists the vendor's models with Bearer auth.
func (v *Vendor) ModelsRequest(ctx context.Context, t *Target) (*http.Request, bool) {
	req, err := newJSONRequest(ctx, http.MethodGet, v.ModelsURL, nil)
	if err != nil {
		return nil, false
	}
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	applyCustom(req, t)
	return req, true
}

// ParseModels reads a listing; each model carries the protocol it should use,
// when the listing says.
func (v *Vendor) ParseModels(body []byte) ([]DiscoveredModel, error) {
	models, err := v.parse(body)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if models[i].Protocol == "" || !v.Serves(models[i].Protocol) {
			models[i].Protocol = v.ProtocolFor("", models[i].ID)
		}
	}
	sortModels(models)
	return models, nil
}

// OpenCode Go's listing carries "recommended_api" and an "api" object keyed by
// the API families the model is served on.
func parseOpenCodeModels(body []byte) ([]DiscoveredModel, error) {
	var out struct {
		Data []struct {
			ID             string                     `json:"id"`
			Name           string                     `json:"name"`
			Type           string                     `json:"type"`
			RecommendedAPI string                     `json:"recommended_api"`
			API            map[string]json.RawMessage `json:"api"`
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
		if m.ID == "" || (m.Type != "" && m.Type != "chat") {
			continue
		}
		models = append(models, DiscoveredModel{ID: m.ID, DisplayName: m.Name, Protocol: openCodeAPI(m.RecommendedAPI, m.API)})
	}
	return models, nil
}

// openCodeAPI maps OpenCode's API family names onto Polyglot protocols.
// Gemini-family models list "interactions" as recommended, but the Go
// endpoint documents only generateContent, so that is what is used.
func openCodeAPI(recommended string, api map[string]json.RawMessage) protocol.Name {
	one := func(name string) protocol.Name {
		switch name {
		case "chat-completions":
			return protocol.OpenAI
		case "responses":
			return protocol.OpenAIResponses
		case "messages":
			return protocol.Anthropic
		case "generate-content":
			return protocol.Gemini
		}
		return ""
	}
	if p := one(recommended); p != "" {
		return p
	}
	for _, name := range []string{"chat-completions", "responses", "messages", "generate-content"} {
		if _, ok := api[name]; ok {
			return one(name)
		}
	}
	return ""
}

// openCodeFallback mirrors what the listing says today. It only matters when
// the listing is unavailable or a model was typed in by hand; every API on
// Go serves every text model (Console translates when the model's own API
// differs), so a wrong guess still works, just less natively.
func openCodeFallback(id string) protocol.Name {
	id = strings.ToLower(id)
	switch {
	case strings.HasPrefix(id, "claude"), strings.HasPrefix(id, "qwen"), id == "minimax-m3":
		return protocol.Anthropic
	case strings.HasPrefix(id, "gpt-"), strings.HasPrefix(id, "grok-"), strings.HasPrefix(id, "muse-spark"):
		return protocol.OpenAIResponses
	case strings.HasPrefix(id, "gemini"):
		return protocol.Gemini
	}
	return protocol.OpenAI
}

// Command Code's listing has "supported_endpoints" per model. Claude models
// list only /messages; the rest list /chat/completions, most also /responses.
func parseCommandCodeModels(body []byte) ([]DiscoveredModel, error) {
	var out struct {
		Data []struct {
			ID                 string   `json:"id"`
			Name               string   `json:"name"`
			SupportedEndpoints []string `json:"supported_endpoints"`
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
		if m.ID == "" {
			continue
		}
		models = append(models, DiscoveredModel{ID: m.ID, DisplayName: m.Name, Protocol: commandCodeAPI(m.ID, m.SupportedEndpoints)})
	}
	return models, nil
}

func commandCodeAPI(id string, endpoints []string) protocol.Name {
	has := map[string]bool{}
	for _, e := range endpoints {
		has[e] = true
	}
	switch {
	case has["/messages"] && !has["/chat/completions"] && !has["/responses"]:
		return protocol.Anthropic
	case has["/responses"] && strings.HasPrefix(strings.ToLower(id), "gpt-"):
		return protocol.OpenAIResponses
	case has["/chat/completions"]:
		return protocol.OpenAI
	case has["/responses"]:
		return protocol.OpenAIResponses
	case has["/messages"]:
		return protocol.Anthropic
	}
	return ""
}

func commandCodeFallback(id string) protocol.Name {
	id = strings.ToLower(id)
	switch {
	case strings.HasPrefix(id, "claude"):
		return protocol.Anthropic
	case strings.HasPrefix(id, "gpt-"):
		return protocol.OpenAIResponses
	}
	return protocol.OpenAI
}
