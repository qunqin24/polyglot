package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/web"
)

// adminBodyLimit caps admin API payloads. The Inspector accepts pasted
// requests, so it is not tiny, but it is far below the gateway limit.
const adminBodyLimit = 4 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !isBrokenPipe(err) {
		// The response is already committed; nothing to do but note it.
		return
	}
}

func isBrokenPipe(err error) bool {
	return errors.Is(err, io.ErrClosedPipe) || strings.Contains(err.Error(), "broken pipe")
}

func writeErr(w http.ResponseWriter, status int, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeProtocolError(w http.ResponseWriter, proto protocol.Name, e *canonical.Error) {
	codec, err := protocol.Get(proto)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status())
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": e.Message})
		return
	}
	w.Write(codec.EncodeError(e))
}

// decodeJSON reads a size-limited JSON body and rejects unknown fields so a
// typo in the WebUI surfaces instead of being ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, adminBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func idParam(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}

// storeErrStatus maps a store error onto an HTTP status.
func storeErrStatus(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// webUIHandler serves the embedded SPA, or proxies to the Vite dev server when
// POLYGLOT_DEV is set.
func (s *Server) webUIHandler() http.HandlerFunc {
	if s.cfg.Dev {
		target, err := url.Parse(s.cfg.DevProxy)
		if err == nil {
			s.log.Info("serving WebUI from the Vite dev server", "url", s.cfg.DevProxy)
			proxy := httputil.NewSingleHostReverseProxy(target)
			return proxy.ServeHTTP
		}
		s.log.Error("invalid POLYGLOT_DEV_PROXY, falling back to the embedded UI", "value", s.cfg.DevProxy)
	}

	assets, err := web.FS()
	if err != nil {
		s.log.Error("embedded WebUI is unavailable", "error", err)
		return func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusInternalServerError, "WebUI assets are missing from this build")
		}
	}
	fileServer := http.FileServer(http.FS(assets))

	return func(w http.ResponseWriter, r *http.Request) {
		// Unmatched API paths must 404 as JSON, not as the SPA shell.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") ||
			strings.HasPrefix(r.URL.Path, "/v1beta/") {
			writeErr(w, http.StatusNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path)
			return
		}

		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}
		if f, err := assets.Open(clean); err == nil {
			f.Close()
			if strings.HasPrefix(clean, "assets/") {
				// Vite fingerprints these filenames, so they are immutable.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Client-side routing: everything else renders the shell.
		serveIndex(w, r, assets)
	}
}

func serveIndex(w http.ResponseWriter, _ *http.Request, assets fs.FS) {
	body, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		// Built without `npm run build`; explain rather than 500.
		body = []byte(web.Placeholder)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
