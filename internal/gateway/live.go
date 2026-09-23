package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/protocol/live"
	"github.com/qunqin24/polyglot/internal/router"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/telemetry"
)

// Live serves the Gemini Live WebSocket protocol. Its first message is decoded
// before the upgrade so routing and key restrictions retain normal HTTP error
// semantics. Once upgraded, every frame goes through canonical.LiveMessage.
func (g *Gateway) Live(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	tel := g.Telemetry.StartRequest(telemetry.RequestInfo{
		ID: telemetry.RequestIDFrom(r.Context()), ClientProtocol: string(live.Name),
		Parent: telemetry.ParentFrom(r.Context()), Method: r.Method, Route: routePattern(r),
	})
	rec := &store.RequestLog{RequestID: tel.ID(), StartedAt: started,
		ClientProtocol: string(live.Name), Status: "error", Stream: true,
		ClientIP: clientIP(r), ClientApp: clientApp(r)}
	key := auth.APIKeyFromContext(r.Context())
	teamID := int64(store.DefaultTeamID)
	if key != nil {
		teamID = key.TeamID
		rec.APIKeyID, rec.APIKeyName, rec.TeamName = &key.ID, key.Name, key.TeamName
	}
	rec.TeamID = teamID
	var lease *auth.QuotaLease
	defer func() {
		g.finish(rec, tel, started)
		lease.Complete(rec.InputTokens + rec.OutputTokens)
	}()
	fail := func(status int, message string) {
		rec.StatusCode = status
		rec.ErrorType = string(canonical.ErrInvalidRequest)
		if status >= 500 {
			rec.ErrorType = string(canonical.ErrUpstream)
		}
		http.Error(w, message, status)
	}
	if g.KeyLimiter != nil {
		var limited *auth.LimitError
		var err error
		lease, limited, err = g.KeyLimiter.Acquire(r.Context(), key)
		if err != nil {
			fail(500, "could not check API key limits")
			return
		}
		if limited != nil {
			fail(429, limited.Error())
			return
		}
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		fail(400, "Gemini Live requires a WebSocket upgrade")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		rec.StatusCode = 400
		return
	}
	defer conn.CloseNow()
	rec.StatusCode = http.StatusSwitchingProtocols
	tel.Streaming(true)
	conn.SetReadLimit(g.Config.MaxRequestBytes)
	firstCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_, first, err := conn.Read(firstCtx)
	cancel()
	if err != nil {
		liveClose(conn, websocket.StatusPolicyViolation, "Live setup required")
		return
	}
	msg, err := live.DecodeClient(first)
	if err != nil || msg.Kind != canonical.LiveSetup {
		liveClose(conn, websocket.StatusUnsupportedData, "invalid Live setup")
		return
	}
	rec.ModelAlias = msg.Model
	if !auth.ModelAllowed(key, msg.Model) {
		liveClose(conn, websocket.StatusPolicyViolation, "model not allowed for API key")
		return
	}
	// Key output-token caps cannot be silently ignored on a Live session.
	if key != nil && key.MaxOutputTokens != nil && *key.MaxOutputTokens > 0 {
		liveClose(conn, websocket.StatusPolicyViolation, "key output-token cap is not supported by Live sessions")
		return
	}
	candidates, err := g.Router.Resolve(r.Context(), g.Store.ForTeam(teamID), msg.Model)
	if err != nil {
		liveClose(conn, websocket.StatusPolicyViolation, "model not found")
		return
	}
	// Live uses the Gemini API's own WebSocket format. Stateless providers do
	// not speak it, even when they offer a model with the same name.
	candidates = router.PreferProtocol(candidates, protocol.Gemini)
	var target *router.Resolution
	for i := range candidates {
		if candidates[i].Target.Protocol == protocol.Gemini {
			target = &candidates[i]
			break
		}
	}
	if target == nil {
		liveClose(conn, websocket.StatusPolicyViolation, "no Gemini Live provider registered for this model")
		return
	}
	rec.ProviderID, rec.ProviderName = &target.Target.ID, target.Target.Name
	rec.UpstreamProtocol, rec.UpstreamModel = string(live.Name), target.UpstreamModel
	attempt := tel.StartAttempt(target.Target.Name, string(live.Name), target.UpstreamModel)
	upstream, err := g.Client.DialLive(r.Context(), target.Target)
	if err != nil {
		attempt.Failed("upstream")
		liveClose(conn, websocket.StatusInternalError, "upstream Live connection failed")
		return
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(g.Config.MaxUpstreamBytes)
	msg.Model = target.UpstreamModel
	clientDiag := canonical.NewDiagnostics()
	setup, err := live.EncodeClient(msg, clientDiag)
	if err != nil || upstream.Write(r.Context(), websocket.MessageText, setup) != nil {
		attempt.Failed("upstream")
		liveClose(conn, websocket.StatusInternalError, "upstream Live setup failed")
		return
	}
	ctx, stop := context.WithCancel(r.Context())
	result := make(chan liveResult, 2)
	go func() { result <- forwardLive(ctx, conn, upstream, true, g.Config.MaxRequestBytes, clientDiag, nil) }()
	go func() { result <- forwardLive(ctx, upstream, conn, false, g.Config.MaxUpstreamBytes, nil, tel) }()
	firstResult := <-result
	stop()
	// Cancelling read/write contexts unblocks both directions even if the
	// other peer never sends a close frame.
	conn.CloseNow()
	upstream.CloseNow()
	secondResult := <-result
	serverResult := firstResult
	if serverResult.client {
		serverResult = secondResult
	}
	if serverResult.usage != nil {
		rec.InputTokens = serverResult.usage.InputTokens
		rec.OutputTokens = serverResult.usage.OutputTokens
		rec.CachedInputTokens = serverResult.usage.CachedInputTokens
		rec.ReasoningTokens = serverResult.usage.ReasoningTokens
		tel.Usage(rec.InputTokens, rec.OutputTokens, rec.ReasoningTokens)
	}
	if len(clientDiag.Notes) > 0 || len(serverResult.notes) > 0 {
		notes := append(clientDiag.All(), serverResult.notes...)
		if b, err := json.Marshal(notes); err == nil {
			rec.FidelityNotes = string(b)
		}
	}
	switch {
	case firstResult.invalid:
		class := "invalid_request"
		rec.ErrorType = string(canonical.ErrInvalidRequest)
		if !firstResult.client {
			class = "upstream"
			rec.ErrorType = string(canonical.ErrUpstream)
		}
		attempt.Failed(class)
		rec.Status = "error"
	case firstResult.err != nil && firstResult.client:
		attempt.Succeeded()
		rec.Status = "cancelled"
	case firstResult.err != nil:
		attempt.Failed("upstream")
		rec.Status = "error"
		rec.ErrorType = string(canonical.ErrUpstream)
	default:
		attempt.Succeeded()
		rec.Status = "success"
	}
}

type liveResult struct {
	client  bool
	err     error
	invalid bool
	usage   *canonical.Usage
	notes   []canonical.Note
}

func forwardLive(ctx context.Context, from, to *websocket.Conn, client bool, limit int64,
	shared *canonical.Diagnostics, tel *telemetry.Request) liveResult {
	result := liveResult{client: client}
	diag := shared
	if diag == nil {
		diag = canonical.NewDiagnostics()
	}
	for {
		kind, raw, err := from.Read(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				result.err = err
			}
			break
		}
		if kind != websocket.MessageText || int64(len(raw)) > limit {
			result.invalid = true
			result.err = fmt.Errorf("invalid Live frame")
			liveClose(from, websocket.StatusUnsupportedData, "invalid Live frame")
			break
		}
		var m *canonical.LiveMessage
		if client {
			m, err = live.DecodeClient(raw)
		} else {
			m, err = live.DecodeServer(raw)
		}
		if err == nil {
			if client && m.Kind == canonical.LiveSetup {
				err = fmt.Errorf("duplicate Live setup")
			}
		}
		if err != nil {
			result.invalid = true
			result.err = err
			liveClose(from, websocket.StatusUnsupportedData, "invalid Live message")
			break
		}
		var out []byte
		if client {
			out, err = live.EncodeClient(m, diag)
		} else {
			out, err = live.EncodeServer(m, diag)
		}
		if err != nil {
			result.invalid, result.err = true, err
			break
		}
		if err = to.Write(ctx, websocket.MessageText, out); err != nil {
			result.err = err
			break
		}
		if !client {
			if m.Usage != nil {
				usage := *m.Usage
				result.usage = &usage
			}
			if m.Kind == canonical.LiveServerContent && m.Output != nil && len(m.Output.Parts) > 0 {
				tel.ContentToken()
			}
		}
	}
	result.notes = diag.All()
	return result
}

func liveClose(c *websocket.Conn, status websocket.StatusCode, reason string) {
	// Close can wait for a peer's close response. Bound the wait by CloseNow
	// at the caller and never include an upstream error in the close reason.
	_ = c.Close(status, reason)
}
