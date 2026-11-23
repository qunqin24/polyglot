package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

// livePing (a variable only so a test can shorten it) keeps an idle connection alive through proxies that close quiet
// ones.
var livePing = 15 * time.Second

// handleLive streams what the gateway is doing right now to the WebUI, as
// Server-Sent Events: one `data: <json>` frame per event, starting with a
// snapshot, and a `: ping` comment every 15 seconds.
//
// It sits in the admin group, so the session is checked before anything is
// sent, and it shows only the team every admin route operates on (see team()).
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	// The listener's write deadline, if the server set one, would cut the
	// stream; a long-lived connection is the whole point here. Not every
	// ResponseWriter supports it, and that is fine.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// no-transform stops a proxy from compressing, which would buffer;
	// X-Accel-Buffering does the same for nginx.
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.liveHub.Subscribe(store.DefaultTeamID)
	defer cancel()

	ping := time.NewTicker(livePing)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
