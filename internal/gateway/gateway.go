// Package gateway is the request pipeline:
//
//	client protocol -> canonical -> routing -> upstream protocol -> upstream
//	upstream -> canonical -> client protocol
//
// Every request takes this path, including OpenAI -> OpenAI. There is no
// special-cased passthrough, so one code path carries all the behaviour.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/media"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/router"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/stream"
	"github.com/qunqin24/polyglot/internal/telemetry"
	"github.com/qunqin24/polyglot/internal/usage"
)

type Gateway struct {
	Store     *store.Store
	Router    *router.Router
	Client    *provider.Client
	Usage     *usage.Logger
	Config    *config.Config
	Log       *slog.Logger
	Telemetry *telemetry.Telemetry
	// Health remembers which upstreams just failed, so a client's own retry
	// is not sent back to the same broken one.
	Health *provider.Health
	// Media downloads attachments a client referenced by URL. Nil unless
	// FETCH_REMOTE_MEDIA is on, in which case a URL bound for a protocol that
	// cannot fetch one is reported instead.
	Media *media.Fetcher
	// KeyLimiter enforces client-key quotas across every protocol endpoint.
	KeyLimiter *auth.KeyLimiter
}

// Options describe the inbound endpoint. Gemini puts the model and the
// streaming choice in the URL rather than the body, hence the overrides.
type Options struct {
	ClientProtocol protocol.Name
	ModelOverride  string
	ForceStream    *bool
}

// Chat runs one request end to end and writes the reply.
func (g *Gateway) Chat(w http.ResponseWriter, r *http.Request, opt Options) {
	started := time.Now()
	tel := g.Telemetry.StartRequest(telemetry.RequestInfo{
		ID:             telemetry.RequestIDFrom(r.Context()),
		ClientProtocol: string(opt.ClientProtocol),
		Parent:         telemetry.ParentFrom(r.Context()),
		Method:         r.Method,
		Route:          routePattern(r),
	})
	rec := &store.RequestLog{
		RequestID:      tel.ID(),
		StartedAt:      started,
		ClientProtocol: string(opt.ClientProtocol),
		Status:         "error",
	}
	if key := auth.APIKeyFromContext(r.Context()); key != nil {
		rec.APIKeyID = &key.ID
		rec.APIKeyName = key.Name
	}
	rec.ClientIP = clientIP(r)
	rec.ClientApp = clientApp(r)
	var lease *auth.QuotaLease
	defer func() {
		g.finish(rec, tel, started)
		if lease != nil {
			lease.Complete(rec.InputTokens + rec.OutputTokens)
		}
	}()

	key := auth.APIKeyFromContext(r.Context())
	if key != nil && g.KeyLimiter != nil {
		var limitErr *auth.LimitError
		var err error
		lease, limitErr, err = g.KeyLimiter.Acquire(r.Context(), key)
		if err != nil {
			g.Log.Error("could not check API key limits", "request_id", rec.RequestID, "error", err)
			g.fail(w, opt.ClientProtocol, rec, canonical.Errorf(canonical.ErrInternal, "could not check API key limits"))
			return
		}
		if limitErr != nil {
			// A rate limit clears on its own, so it is a 429 and says when to
			// come back. A spent budget with no window to roll over does not:
			// telling a client to retry a key that will never work again is
			// worse than telling it the key may not spend any more.
			if limitErr.RetryAfter > 0 {
				seconds := int((limitErr.RetryAfter + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", fmt.Sprint(seconds))
			}
			errType, code := canonical.ErrRateLimit, "api_key_limit_exceeded"
			if limitErr.Budget > 0 {
				code = "api_key_budget_exhausted"
				if limitErr.RetryAfter <= 0 {
					errType = canonical.ErrPermission
				}
			}
			g.fail(w, opt.ClientProtocol, rec, &canonical.Error{Type: errType,
				Code: code, Message: limitErr.Error()})
			return
		}
	}

	clientCodec, err := protocol.Get(opt.ClientProtocol)
	if err != nil {
		g.fail(w, opt.ClientProtocol, rec, toCanonicalError(err))
		return
	}

	body, cerr := readBody(w, r, g.Config.MaxRequestBytes)
	if cerr != nil {
		g.fail(w, opt.ClientProtocol, rec, cerr)
		return
	}

	diag := canonical.NewDiagnostics()
	creq, err := clientCodec.DecodeRequest(body, diag)
	if err != nil {
		g.fail(w, opt.ClientProtocol, rec, toCanonicalError(err))
		return
	}
	if opt.ModelOverride != "" {
		creq.Model = opt.ModelOverride
	}
	if opt.ForceStream != nil {
		creq.Stream = *opt.ForceStream
	}
	rec.ModelAlias = creq.Model
	if cerr := auth.ApplyRequestPolicy(key, creq); cerr != nil {
		g.fail(w, opt.ClientProtocol, rec, cerr)
		return
	}
	rec.Stream = creq.Stream
	// The client's own labels, known only once the body is decoded. Three of
	// the five protocols carry them and they were being dropped at the end of
	// the request until now.
	rec.RequestUser = sanitizeApp(creq.User)
	rec.RequestMetadata = requestLabels(creq)
	tel.Streaming(creq.Stream)

	route := tel.StartSpan("router.resolve")
	candidates, err := g.Router.Resolve(r.Context(), creq.Model)
	if err != nil {
		// The model name a client asked for is not recorded on the span: an
		// unresolved name is client-supplied text.
		route.SetError(telemetry.ErrorClass(toCanonicalError(err)))
		route.End()
		g.fail(w, opt.ClientProtocol, rec, toCanonicalError(err))
		return
	}
	// Among providers the operator ranked equally, prefer one that speaks the
	// client's own protocol: that route forwards a provider's own parameters
	// and built-in tools instead of reporting them. It only ever reorders
	// within a priority level — an inferred preference must not overrule a
	// priority the operator set deliberately.
	candidates = router.PreferProtocol(candidates, opt.ClientProtocol)
	candidates = g.healthy(candidates, diag)
	route.SetAttributes(telemetry.IntAttr("polyglot.candidates", int64(len(candidates))))
	route.End()

	var lastErr *canonical.Error
	for i, cand := range candidates {
		attempt := &attempt{
			gw:          g,
			clientCodec: clientCodec,
			diag:        diag,
			creq:        creq,
			cand:        cand,
			rec:         rec,
			tel:         tel,
		}
		res := attempt.run(w, r)
		if res.err == nil {
			return
		}
		lastErr = res.err
		// Once bytes have reached the client we cannot start over, and a
		// deliberate refusal from upstream is not worth retrying either.
		if res.wrote || !retryable(res.err) || i == len(candidates)-1 {
			break
		}
		next := candidates[i+1]
		tel.Retrying(attempt.tried, telemetry.ErrorClass(res.err), next.Target.Name != cand.Target.Name)
		g.Log.Warn("upstream failed, trying fallback",
			"request_id", rec.RequestID,
			"provider", cand.Target.Name, "model", cand.UpstreamModel, "error", res.err.Message)
	}
	if lastErr == nil {
		lastErr = canonical.Errorf(canonical.ErrInternal, "no upstream candidate was attempted")
	}
	g.fail(w, opt.ClientProtocol, rec, lastErr)
}

// healthy drops candidates that are in cooldown.
//
// If that would leave nothing, the cooldowns are ignored and the original list
// is used. A brief outage that touches every provider must not lock the
// gateway out of its own upstreams — better to fail one request against a
// provider that may have recovered than to refuse everything while it has.
func (g *Gateway) healthy(cands []router.Resolution, d *canonical.Diagnostics) []router.Resolution {
	if g.Health == nil || len(cands) < 2 {
		// With a single candidate there is nothing to skip to, so a cooldown
		// would only mean refusing to try the one provider that exists.
		return cands
	}
	out := make([]router.Resolution, 0, len(cands))
	var skipped []string
	for _, c := range cands {
		if g.Health.Available(c.Target.ID) {
			out = append(out, c)
			continue
		}
		skipped = append(skipped, c.Target.Name)
	}
	if len(out) == 0 {
		return cands
	}
	if len(skipped) > 0 {
		d.WithStage("route").Note("provider", canonical.FidelitySemantic,
			"provider(s) %s were skipped: they failed recently and are in cooldown",
			strings.Join(skipped, ", "))
	}
	return out
}

// clientIP is the address the request came from, without the port.
//
// It is r.RemoteAddr — the actual TCP peer — unless TRUST_PROXY_HEADERS is on,
// in which case the RealIP middleware has already replaced it with the value a
// trusted proxy supplied. Anything a client can set for itself must not end up
// here: a forgeable address in the log would answer the "has my key leaked"
// question with whatever the intruder preferred.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// routePattern reports the chi route template rather than the concrete path,
// so a span attribute stays a bounded value and never carries a query string —
// which is one of the places an api_key= parameter shows up.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		return rc.RoutePattern()
	}
	return r.URL.Path
}

type attemptResult struct {
	err   *canonical.Error
	wrote bool // response bytes already reached the client
}

type attempt struct {
	gw          *Gateway
	clientCodec protocol.Codec
	diag        *canonical.Diagnostics
	creq        *canonical.Request
	cand        router.Resolution
	rec         *store.RequestLog
	tel         *telemetry.Request
	// tried is this attempt's telemetry span, kept so the caller can attribute
	// a retry to the provider that failed.
	tried *telemetry.Attempt
}

// fail closes the attempt's telemetry with an error class and returns the
// result, so every early return from run reports the attempt exactly once.
func (a *attempt) failed(err *canonical.Error, wrote bool) attemptResult {
	a.tried.Failed(telemetry.ErrorClass(err))
	a.gw.noteFailure(a.cand.Target, err)
	return attemptResult{err: err, wrote: wrote}
}

// noteFailure updates what is remembered about an upstream, and takes it out
// of rotation when its credential has clearly stopped working.
//
// A bad request is the caller's fault and says nothing about the provider, so
// it is not held against it: marking a healthy provider unhealthy because a
// client sent malformed JSON would be worse than not tracking health at all.
func (g *Gateway) noteFailure(t *provider.Target, err *canonical.Error) {
	if g.Health == nil || err == nil {
		return
	}
	switch err.Type {
	case canonical.ErrInvalidRequest, canonical.ErrNotFound, canonical.ErrUnsupported:
		return
	}

	authFailure := err.Type == canonical.ErrAuthentication || err.Type == canonical.ErrPermission
	strikes := g.Health.Failed(t.ID, authFailure)

	if !authFailure || !t.AutoDisableOnAuthError || strikes < provider.AuthStrikesBeforeDisable {
		return
	}
	// A rejected credential does not heal on its own: cooling down and trying
	// again every thirty seconds would be a permanent trickle of failures.
	reason := fmt.Sprintf("disabled automatically after %d consecutive credential rejections: %s",
		strikes, err.Message)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if dbErr := g.Store.DisableProvider(ctx, t.ID, reason); dbErr != nil {
		g.Log.Error("could not disable provider after auth failures", "provider", t.Name, "error", dbErr)
		return
	}
	g.Log.Error("provider disabled automatically: its credential was rejected",
		"provider", t.Name, "strikes", strikes)
	g.Health.Forget(t.ID)
}

func (a *attempt) run(w http.ResponseWriter, r *http.Request) attemptResult {
	g := a.gw
	t := a.cand.Target

	a.rec.ProviderID = &t.ID
	a.rec.ProviderName = t.Name
	a.rec.UpstreamProtocol = string(t.Protocol)
	a.rec.UpstreamModel = a.cand.UpstreamModel
	a.tried = a.tel.StartAttempt(t.Name, string(t.Protocol), a.cand.UpstreamModel)

	upCodec, err := protocol.Get(t.Protocol)
	if err != nil {
		return a.failed(toCanonicalError(err), false)
	}

	// The upstream request carries the upstream model name, not the alias.
	// This is a copy, so an attempt against a second provider is unaffected by
	// what the first one was allowed to carry.
	upReq := *a.creq
	upReq.Model = a.cand.UpstreamModel

	// An upstream that rejects members it does not know gets the recognised
	// fields only. The codec then reports the rest as unsupported rather than
	// sending them, which is the same answer a cross-protocol route gives.
	if t.StrictFields && upReq.Extensions.Len() > 0 {
		a.diag.WithStage("route").Note("extensions", canonical.FidelityUnsupported,
			"provider %q is in strict-fields mode; %d unrecognised field(s) were not sent: %s",
			t.Name, upReq.Extensions.Len(), upReq.Extensions.Summary())
		upReq.Extensions = nil
	}

	// Gemini does not fetch a remote attachment itself. When the operator has
	// allowed it, download the bytes here so the image survives; otherwise the
	// codec below reports it, which is the honest default.
	if g.Media != nil && !protocol.AcceptsMediaURL(t.Protocol) {
		g.Media.Inline(r.Context(), &upReq, a.diag.WithStage("media"))
	}

	upBody, err := upCodec.EncodeRequest(&upReq, a.diag)
	if err != nil {
		return a.failed(toCanonicalError(err), false)
	}

	driver, err := provider.DriverFor(t.Protocol)
	if err != nil {
		return a.failed(canonical.Errorf(canonical.ErrInternal, "%v", err), false)
	}

	// The upstream call inherits the client's cancellation, so a disconnected
	// client tears down the upstream connection instead of leaking it.
	ctx, cancel := context.WithTimeout(r.Context(), t.Timeout)
	defer cancel()

	httpReq, err := driver.ChatRequest(ctx, t, a.cand.UpstreamModel, upBody, upReq.Stream)
	if err != nil {
		return a.failed(canonical.Errorf(canonical.ErrInternal, "%v", err), false)
	}

	resp, err := g.Client.Do(httpReq)
	if err != nil {
		return a.failed(transportError(err, t), false)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return a.failed(g.upstreamError(resp, t), false)
	}

	if upReq.Stream {
		return a.streamResponse(w, r, resp, upCodec)
	}
	return a.bufferedResponse(w, resp, upCodec)
}

func (a *attempt) bufferedResponse(w http.ResponseWriter, resp *http.Response, upCodec protocol.Codec) attemptResult {
	g := a.gw
	raw, err := io.ReadAll(io.LimitReader(resp.Body, g.Config.MaxUpstreamBytes))
	if err != nil {
		return a.failed(transportError(err, a.cand.Target), false)
	}

	cresp, err := upCodec.DecodeResponse(raw, a.diag)
	if err != nil {
		return a.failed(toCanonicalError(err), false)
	}
	// Report the alias the client asked for, not the upstream name.
	cresp.Model = a.creq.Model

	out, err := a.clientCodec.EncodeResponse(cresp, a.creq, a.diag)
	if err != nil {
		return a.failed(toCanonicalError(err), false)
	}

	a.rec.InputTokens = cresp.Usage.InputTokens
	a.rec.OutputTokens = cresp.Usage.OutputTokens
	a.rec.ReasoningTokens = cresp.Usage.ReasoningTokens
	// Parts of InputTokens, not additions to it — the log stores what
	// canonical.Usage means, so a hit rate is one column over the other.
	a.rec.CachedInputTokens = cresp.Usage.CachedInputTokens
	a.rec.CacheWriteTokens = cresp.Usage.CacheWriteTokens
	a.rec.Status = "success"
	a.rec.StatusCode = http.StatusOK
	a.rec.FidelityNotes = encodeNotes(a.diag)
	a.gw.Health.Succeeded(a.cand.Target.ID)
	a.tel.Usage(cresp.Usage.InputTokens, cresp.Usage.OutputTokens, cresp.Usage.ReasoningTokens)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Polyglot-Provider", a.cand.Target.Name)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(out); err != nil {
		return a.failed(canonical.Errorf(canonical.ErrInternal, "write response: %v", err), true)
	}
	a.tried.Succeeded()
	return attemptResult{}
}

func (a *attempt) streamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, upCodec protocol.Codec) attemptResult {
	g := a.gw

	flusher, ok := w.(http.Flusher)
	if !ok {
		return a.failed(canonical.Errorf(canonical.ErrInternal, "streaming is not supported by this server"), false)
	}

	stream.SetSSEHeaders(w.Header())
	w.Header().Set("X-Polyglot-Provider", a.cand.Target.Name)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := a.clientCodec.NewStreamEncoder(w, a.creq)
	acc := canonical.NewAccumulator()

	err := upCodec.DecodeStream(r.Context(), resp.Body, func(ev *canonical.Event) error {
		if ev.Type == canonical.EventNative && ev.Native != nil &&
			ev.Native.Protocol != string(a.clientCodec.Name()) {
			a.diag.Note("stream."+ev.Native.Name, canonical.FidelityUnsupported,
				"native %s stream event cannot be expressed in %s and was not forwarded",
				ev.Native.Protocol, a.clientCodec.Name())
			return nil
		}
		// The only telemetry on the streaming hot path: one type comparison
		// and, for content chunks, a clock read. Nothing is buffered, no chunk
		// is inspected, and the event is passed on untouched.
		if isContent(ev) {
			a.tel.ContentToken()
		}
		// Report the alias the client asked for, not the upstream name, so a
		// streamed reply matches a buffered one.
		if ev.Type == canonical.EventMessageStart && ev.Model != "" {
			ev.Model = a.creq.Model
		}
		acc.Add(ev)
		if err := enc.Write(ev); err != nil {
			// The client hung up; stop reading upstream immediately.
			return fmt.Errorf("write to client: %w", err)
		}
		return nil
	})

	final := acc.Response()
	a.rec.InputTokens = final.Usage.InputTokens
	a.rec.OutputTokens = final.Usage.OutputTokens
	a.rec.ReasoningTokens = final.Usage.ReasoningTokens
	// Parts of InputTokens, not additions to it — the log stores what
	// canonical.Usage means, so a hit rate is one column over the other.
	a.rec.CachedInputTokens = final.Usage.CachedInputTokens
	a.rec.CacheWriteTokens = final.Usage.CacheWriteTokens
	a.rec.FidelityNotes = encodeNotes(a.diag)
	a.tel.Usage(final.Usage.InputTokens, final.Usage.OutputTokens, final.Usage.ReasoningTokens)

	if err != nil {
		if provider.IsClientDisconnect(err) || errors.Is(r.Context().Err(), context.Canceled) {
			a.rec.Status = "cancelled"
			a.rec.StatusCode = 499 // nginx's "client closed request"
			a.rec.ErrorType = "client_disconnect"
			// A caller that hung up is not an upstream failure, and counting
			// it as one would make every cancelled agent look like an outage.
			a.tried.Failed("cancelled")
			return attemptResult{}
		}
		cerr := toCanonicalError(err)
		a.rec.Status = "error"
		a.rec.StatusCode = http.StatusOK // headers already sent
		a.rec.ErrorType = string(cerr.Type)
		a.rec.ErrorMessage = cerr.Message
		// Report the failure inside the stream: the status line is long gone.
		_ = enc.Write(&canonical.Event{Type: canonical.EventError, Error: cerr})
		_ = enc.Close()
		g.Log.Warn("stream failed after headers were sent",
			"request_id", a.rec.RequestID, "provider", a.cand.Target.Name, "error", cerr.Message)
		return a.failed(cerr, true)
	}

	if err := enc.Close(); err != nil {
		a.rec.Status = "cancelled"
		a.rec.StatusCode = 499
		a.tried.Failed("cancelled")
		return attemptResult{}
	}
	a.rec.Status = "success"
	a.rec.StatusCode = http.StatusOK
	a.tried.Succeeded()
	return attemptResult{}
}

// isContent reports whether an event carries model output, as opposed to
// framing. TTFT means the first token a user could see, so message.start —
// which arrives before the model has produced anything — does not count, and
// neither does a usage or end event.
func isContent(ev *canonical.Event) bool {
	switch ev.Type {
	case canonical.EventTextDelta, canonical.EventReasoningDelta:
		// Replay signatures and other metadata can arrive on an empty delta.
		// They matter to protocol fidelity but contain no generated token, so
		// counting them creates a fake second timestamp for one-chunk replies.
		return ev.Text != ""
	case canonical.EventToolCallStart, canonical.EventToolCallDelta:
		return true
	}
	return false
}

// --- errors ---------------------------------------------------------------

func (g *Gateway) fail(w http.ResponseWriter, proto protocol.Name, rec *store.RequestLog, cerr *canonical.Error) {
	rec.Status = "error"
	rec.StatusCode = cerr.Status()
	rec.ErrorType = string(cerr.Type)
	rec.ErrorMessage = cerr.Message

	codec, err := protocol.Get(proto)
	if err != nil {
		http.Error(w, cerr.Message, cerr.Status())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cerr.Status())
	w.Write(codec.EncodeError(cerr))
}

// upstreamError converts a non-2xx upstream reply into a canonical error,
// stripping anything that could leak the provider credential.
func (g *Gateway) upstreamError(resp *http.Response, t *provider.Target) *canonical.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := extractUpstreamMessage(raw)
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	msg = redact(msg, t.APIKey)
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}

	g.Log.Warn("upstream error",
		"provider", t.Name, "status", resp.StatusCode, "message", msg)

	return &canonical.Error{
		Type:       canonical.TypeForStatus(resp.StatusCode),
		Message:    fmt.Sprintf("upstream %s returned %d: %s", t.Name, resp.StatusCode, msg),
		StatusCode: resp.StatusCode,
	}
}

// extractUpstreamMessage reads the error text out of any of the three
// protocols' error envelopes, all of which nest a message under "error".
func extractUpstreamMessage(raw []byte) string {
	var probe struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// Gemini sometimes replies with a top-level array of errors.
		var arr []struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
			return arr[0].Error.Message
		}
		return ""
	}
	for _, s := range []string{probe.Error.Message, probe.Message, probe.Detail, probe.Error.Status} {
		if s != "" {
			return s
		}
	}
	return ""
}

// redact removes a secret from a string that is about to be shown to a client
// or written to a log.
func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) < 8 {
			continue
		}
		s = strings.ReplaceAll(s, sec, "***")
	}
	return s
}

func transportError(err error, t *provider.Target) *canonical.Error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return canonical.Errorf(canonical.ErrTimeout, "upstream %s timed out after %s", t.Name, t.Timeout)
	case errors.Is(err, context.Canceled):
		return &canonical.Error{Type: canonical.ErrInternal, Message: "client closed the request", StatusCode: 499}
	default:
		return canonical.Errorf(canonical.ErrUpstream, "cannot reach upstream %s: %s", t.Name, redact(err.Error(), t.APIKey))
	}
}

func toCanonicalError(err error) *canonical.Error {
	var ce *canonical.Error
	if errors.As(err, &ce) {
		return ce
	}
	return canonical.Errorf(canonical.ErrInternal, "%s", err.Error())
}

func retryable(e *canonical.Error) bool {
	switch e.Type {
	case canonical.ErrUpstream, canonical.ErrOverloaded, canonical.ErrTimeout, canonical.ErrRateLimit:
		return true
	}
	return e.StatusCode >= 500
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *canonical.Error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &canonical.Error{
				Type:       canonical.ErrInvalidRequest,
				Message:    fmt.Sprintf("request body exceeds the %d byte limit", limit),
				StatusCode: http.StatusRequestEntityTooLarge,
			}
		}
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "read request body: %v", err)
	}
	if len(body) == 0 {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "request body is empty")
	}
	return body, nil
}

func encodeNotes(d *canonical.Diagnostics) string {
	notes := d.All()
	if len(notes) == 0 {
		return ""
	}
	b, err := json.Marshal(notes)
	if err != nil {
		return ""
	}
	return string(b)
}

// finish closes both records of the request: the telemetry lifecycle and the
// row in the request log. They are filled from the same numbers, so the
// Prometheus histogram and the log detail can never tell different stories.
func (g *Gateway) finish(rec *store.RequestLog, tel *telemetry.Request, started time.Time) {
	rec.FinishedAt = time.Now()
	rec.LatencyMS = rec.FinishedAt.Sub(started).Milliseconds()
	if rec.StatusCode == 0 {
		rec.StatusCode = http.StatusInternalServerError
	}

	class := ""
	if rec.Status != "success" {
		class = errorClassFor(rec)
	}
	out := tel.Finish(rec.Status, rec.StatusCode, class)
	rec.TTFTMS = out.TTFTMS
	rec.GenerationMS = out.GenerationMS
	rec.OutputTPS = out.TPS
	rec.RetryCount = out.RetryCount
	rec.FallbackCount = out.FallbackCount

	g.Usage.Log(rec)
}

// errorClassFor recovers the bounded error class from a finished log row. The
// row keeps the canonical type; a cancelled request is its own class rather
// than a failure, because a client that hung up is not an upstream problem.
func errorClassFor(rec *store.RequestLog) string {
	if rec.Status == "cancelled" {
		return "cancelled"
	}
	return telemetry.ErrorClass(&canonical.Error{
		Type:       canonical.ErrorType(rec.ErrorType),
		StatusCode: rec.StatusCode,
	})
}
