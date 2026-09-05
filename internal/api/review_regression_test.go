package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRegressionReviewUpstreamStreamErrorIsFailure(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n")
	}, "openai")
	body := readAll(t, h.post("/v1/responses", `{"model":"my-model","input":"hi","stream":true}`, nil))
	if strings.Contains(body, "response.completed") {
		t.Errorf("error stream also emits response.completed: %s", body)
	}
	if strings.Count(body, "event: error\n") != 1 {
		t.Errorf("expected one error event: %s", body)
	}
	log := h.waitForLog(t)
	if log.Status != "error" {
		t.Errorf("error stream logged as %q", log.Status)
	}
}
