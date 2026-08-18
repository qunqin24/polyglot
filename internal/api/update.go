package api

import (
	"net/http"
	"strings"
)

// handleUpdateStatus compares this build with the public release tags. It is
// deliberately behind the admin session: a gateway client never causes an
// outbound request, and a disabled checker remains entirely local.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	force := strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	writeJSON(w, http.StatusOK, s.updates.Check(r.Context(), force))
}
