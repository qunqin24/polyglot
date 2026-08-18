package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/gateway"
	"github.com/qunqin24/polyglot/internal/protocol"
)

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	s.gw.Chat(w, r, gateway.Options{ClientProtocol: protocol.OpenAI})
}

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	s.gw.Chat(w, r, gateway.Options{ClientProtocol: protocol.OpenAIResponses})
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.gw.Chat(w, r, gateway.Options{ClientProtocol: protocol.Anthropic})
}

// handleGeminiGenerate serves both generateContent and streamGenerateContent.
// Gemini encodes the model and the method in one path segment,
// e.g. /v1beta/models/gemini-2.0-flash:streamGenerateContent
func (s *Server) handleGeminiGenerate(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	// Split on the LAST colon, not the first: Polyglot's own namespace syntax
	// puts colons in the model name (provider::model), and no Gemini method
	// name contains one.
	model, method, ok := cutLast(action, ":")
	if !ok || model == "" {
		writeProtocolError(w, protocol.Gemini, canonical.Errorf(canonical.ErrInvalidRequest,
			"expected a path of the form /v1beta/models/{model}:generateContent"))
		return
	}

	var stream bool
	switch method {
	case "generateContent":
		stream = false
	case "streamGenerateContent":
		stream = true
	case "countTokens", "embedContent", "batchEmbedContents":
		writeProtocolError(w, protocol.Gemini, canonical.Errorf(canonical.ErrUnsupported,
			"Polyglot does not implement %q yet; it converts content generation only", method))
		return
	default:
		writeProtocolError(w, protocol.Gemini, canonical.Errorf(canonical.ErrInvalidRequest,
			"unknown Gemini method %q", method))
		return
	}

	s.gw.Chat(w, r, gateway.Options{
		ClientProtocol: protocol.Gemini,
		ModelOverride:  model,
		ForceStream:    &stream,
	})
}

// handleGeminiListModels answers Gemini's own model listing shape.
func (s *Server) handleGeminiListModels(w http.ResponseWriter, r *http.Request) {
	entries, err := s.router.ListModels(r.Context())
	if err != nil {
		writeProtocolError(w, protocol.Gemini, canonical.Errorf(canonical.ErrInternal, "%v", err))
		return
	}
	models := make([]map[string]any, 0, len(entries))
	key := auth.APIKeyFromContext(r.Context())
	for _, e := range entries {
		if !auth.ModelAllowed(key, e.ID) {
			continue
		}
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"displayName":                e.ID,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleClientModelList answers /v1/models. The payload carries the OpenAI
// fields and Anthropic's, so either SDK can read it from the same endpoint.
func (s *Server) handleClientModelList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.router.ListModels(r.Context())
	if err != nil {
		writeProtocolError(w, protocol.OpenAI, canonical.Errorf(canonical.ErrInternal, "%v", err))
		return
	}
	data := make([]map[string]any, 0, len(entries))
	key := auth.APIKeyFromContext(r.Context())
	for _, e := range entries {
		if !auth.ModelAllowed(key, e.ID) {
			continue
		}
		data = append(data, modelPayload(e.ID, e.Created))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":   "list",
		"data":     data,
		"has_more": false,
	})
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "model")
	entries, err := s.router.ListModels(r.Context())
	if err != nil {
		writeProtocolError(w, protocol.OpenAI, canonical.Errorf(canonical.ErrInternal, "%v", err))
		return
	}
	for _, e := range entries {
		if e.ID == alias && auth.ModelAllowed(auth.APIKeyFromContext(r.Context()), e.ID) {
			writeJSON(w, http.StatusOK, modelPayload(e.ID, e.Created))
			return
		}
	}
	writeProtocolError(w, protocol.OpenAI, canonical.Errorf(canonical.ErrNotFound, "model %q not found", alias))
}

func modelPayload(alias string, created time.Time) map[string]any {
	if created.IsZero() {
		created = time.Now()
	}
	return map[string]any{
		"id":           alias,
		"object":       "model",
		"created":      created.Unix(),
		"owned_by":     "polyglot",
		"type":         "model", // Anthropic
		"display_name": alias,   // Anthropic
	}
}

// cutLast is strings.Cut anchored at the last separator instead of the first.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// handleGeminiInteractions serves Google's Interactions API. One path covers
// streaming and non-streaming, because unlike generateContent the choice is a
// field in the body rather than a different method name.
func (s *Server) handleGeminiInteractions(w http.ResponseWriter, r *http.Request) {
	s.gw.Chat(w, r, gateway.Options{ClientProtocol: protocol.GeminiInteractions})
}
