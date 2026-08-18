package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/store"
)

// Attribution answers "what was this call for". The columns beside it record
// what a request used; none of them records what it was used for.

func attributed(t *testing.T, headers map[string]string, body string) *store.RequestLog {
	t.Helper()
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")
	if body == "" {
		body = `{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`
	}
	readAll(t, h.post("/v1/chat/completions", body, headers))
	return h.waitForLog(t)
}

// The case that needs no client change at all, and so works retroactively.
func TestUserAgentIdentifiesTheCaller(t *testing.T) {
	log := attributed(t, map[string]string{
		"User-Agent": "OpenAI/Python 1.99.0 (Linux; x86_64; build 5)",
	}, "")

	if log.ClientApp != "OpenAI/Python 1.99.0" {
		t.Errorf("client_app = %q, want the product token without the platform detail", log.ClientApp)
	}
}

// OpenRouter's convention, so tools already written for it work unchanged.
func TestAppTitleWinsOverEverythingElse(t *testing.T) {
	log := attributed(t, map[string]string{
		"X-Title":      "docs-site",
		"HTTP-Referer": "https://example.com/app",
		"User-Agent":   "curl/8.4.0",
	}, "")

	if log.ClientApp != "docs-site" {
		t.Errorf("client_app = %q, want the name the caller chose", log.ClientApp)
	}
}

func TestRefererFallsBackToItsHost(t *testing.T) {
	log := attributed(t, map[string]string{
		"HTTP-Referer": "https://myapp.example.com/some/path?token=abc",
		"User-Agent":   "curl/8.4.0",
	}, "")

	if log.ClientApp != "myapp.example.com" {
		t.Errorf("client_app = %q, want just the host", log.ClientApp)
	}
	// The path and query are dropped, so a token in a URL cannot ride along.
	if strings.Contains(log.ClientApp, "token") {
		t.Errorf("a referer query string reached the log: %q", log.ClientApp)
	}
}

// The precise answer when you control the caller.
func TestClientLabelsAreRecorded(t *testing.T) {
	log := attributed(t, nil, `{
		"model": "my-model",
		"messages": [{"role": "user", "content": "hi"}],
		"user": "qunqin",
		"metadata": {"project": "docs-site", "task": "summarise"}
	}`)

	if log.RequestUser != "qunqin" {
		t.Errorf("request_user = %q", log.RequestUser)
	}
	// Sorted keys, so two identical requests store identical strings.
	want := `{"project":"docs-site","task":"summarise"}`
	if log.RequestMetadata != want {
		t.Errorf("request_metadata = %q, want %q", log.RequestMetadata, want)
	}
}

// The rule this feature had to be built around: reading named headers must
// never become reading the headers.
func TestNoCredentialReachesTheLog(t *testing.T) {
	log := attributed(t, map[string]string{
		"User-Agent":   "curl/8.4.0",
		"X-Api-Key":    "sk-should-never-be-stored",
		"Cookie":       "session=should-never-be-stored",
		"X-Real-Thing": "should-never-be-stored",
	}, "")

	joined := log.ClientApp + log.RequestUser + log.RequestMetadata + log.ErrorMessage
	if strings.Contains(joined, "should-never-be-stored") {
		t.Fatalf("a header that was not asked for reached the log: %q", joined)
	}
	// The client's own API key is never stored either — only its name.
	if strings.Contains(joined, "pg_") {
		t.Fatalf("an API key reached the log: %q", joined)
	}
}

// A caller that says nothing gets an empty attribution rather than a guess.
func TestAnUnattributedRequestStaysEmpty(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	// Go sets a default User-Agent unless it is explicitly blanked.
	req.Header.Set("User-Agent", "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	log := h.waitForLog(t)
	if log.ClientApp != "" {
		t.Errorf("client_app = %q, want empty for a caller that identified nothing", log.ClientApp)
	}
}

// Filtering is the point: one app's traffic, isolated.
func TestLogsCanBeFilteredByApp(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"X-Title": "docs-site"}))
	h.waitForLog(t)

	admin := h.adminSession(t)
	var out struct {
		Logs []*store.RequestLog `json:"logs"`
	}
	admin.get(t, "/api/logs?client_app=docs-site", &out)
	if len(out.Logs) == 0 {
		t.Error("filtering by app name returned nothing")
	}
	admin.get(t, "/api/logs?client_app=some-other-app", &out)
	if len(out.Logs) != 0 {
		t.Errorf("an app with no traffic returned %d rows", len(out.Logs))
	}
}
