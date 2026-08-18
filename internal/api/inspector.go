package api

import (
	"encoding/json"
	"net/http"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

// Protocol Inspector: run a request through the conversion pipeline and show
// every stage, without sending anything upstream. This is how you debug what
// Polyglot actually does to a request — and the fastest way to see where a
// conversion loses information.
type inspectRequest struct {
	// InputProtocol is what the pasted body is written in.
	InputProtocol string `json:"input_protocol"`
	// OutputProtocol forces a target protocol. Leave empty to let the model
	// alias decide, exactly as a real request would.
	OutputProtocol string `json:"output_protocol"`
	// UseRouting resolves the model alias against the configured providers.
	UseRouting bool `json:"use_routing"`
	// Body is the request JSON to convert.
	Body json.RawMessage `json:"body"`
	// Model overrides the model in the body (Gemini puts it in the URL).
	Model string `json:"model"`
}

type inspectResponse struct {
	Canonical json.RawMessage  `json:"canonical"`
	Outgoing  json.RawMessage  `json:"outgoing"`
	Notes     []canonical.Note `json:"notes"`
	Route     *inspectRoute    `json:"route,omitempty"`
	Lossy     bool             `json:"lossy"`
}

type inspectRoute struct {
	Provider      string `json:"provider"`
	Protocol      string `json:"protocol"`
	UpstreamModel string `json:"upstream_model"`
	Alias         string `json:"alias"`
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	var in inspectRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if len(in.Body) == 0 {
		writeErr(w, http.StatusBadRequest, "body is required")
		return
	}

	inCodec, err := protocol.Get(protocol.Name(in.InputProtocol))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown input protocol %q", in.InputProtocol)
		return
	}

	diag := canonical.NewDiagnostics()
	creq, err := inCodec.DecodeRequest(in.Body, diag)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot decode the request as %s: %v", in.InputProtocol, err)
		return
	}
	if in.Model != "" {
		creq.Model = in.Model
	}

	out := inspectResponse{}
	var route *inspectRoute
	targetProto := protocol.Name(in.OutputProtocol)

	if in.UseRouting {
		cands, err := s.router.Resolve(r.Context(), creq.Model)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "%v", err)
			return
		}
		best := cands[0]
		route = &inspectRoute{
			Provider:      best.Target.Name,
			Protocol:      string(best.Target.Protocol),
			UpstreamModel: best.UpstreamModel,
			Alias:         creq.Model,
		}
		targetProto = best.Target.Protocol
		creq.Model = best.UpstreamModel
	}
	if targetProto == "" {
		targetProto = protocol.Name(in.InputProtocol)
	}

	// Snapshot the canonical form before the encoder runs, so the middle pane
	// shows the request as the hub understood it.
	canonJSON, err := json.MarshalIndent(creq, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode canonical: %v", err)
		return
	}
	out.Canonical = canonJSON

	outCodec, err := protocol.Get(targetProto)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown output protocol %q", targetProto)
		return
	}
	encoded, err := outCodec.EncodeRequest(creq, diag)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot encode the request as %s: %v", targetProto, err)
		return
	}
	out.Outgoing = indentJSON(encoded)
	out.Notes = diag.All()
	if out.Notes == nil {
		out.Notes = []canonical.Note{}
	}
	out.Lossy = diag.Lossy()
	out.Route = route

	writeJSON(w, http.StatusOK, out)
}

func indentJSON(b []byte) json.RawMessage {
	var pretty json.RawMessage
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return b
	}
	return pretty
}
