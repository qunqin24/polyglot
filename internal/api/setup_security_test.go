package api

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/qunqin24/polyglot/internal/setup"
)

func postSetup(t *testing.T, h *harness, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/setup", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build setup request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(setup.HeaderName, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("setup request: %v", err)
	}
	return resp
}

func TestSetupRequiresTheOneTimeToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "wrong", token: "this-is-the-wrong-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
			resp := postSetup(t, h, tc.token,
				`{"username":"admin","password":"a-good-password"}`)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("setup = %d, want 403: %s", resp.StatusCode, body)
			}
			if n, err := h.store.AdminCount(t.Context()); err != nil || n != 0 {
				t.Fatalf("admin count after rejected setup = %d, %v", n, err)
			}
		})
	}
}

func TestSetupTokenIsOneTime(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	body := `{"username":"admin","password":"a-good-password"}`
	first := postSetup(t, h, testSetupToken, body)
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first setup = %d, want 201", first.StatusCode)
	}

	second := postSetup(t, h, testSetupToken, body)
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		got, _ := io.ReadAll(second.Body)
		t.Fatalf("replayed setup = %d, want 409: %s", second.StatusCode, got)
	}
}

func TestConcurrentSetupCreatesExactlyOneAdministrator(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	start := make(chan struct{})
	type result struct {
		status int
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, username := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/setup", strings.NewReader(
				`{"username":"`+username+`","password":"a-good-password"}`))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(setup.HeaderName, testSetupToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			resp.Body.Close()
			results <- result{status: resp.StatusCode}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	seen := map[int]int{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent setup request: %v", result.err)
		}
		seen[result.status]++
	}
	if seen[http.StatusCreated] != 1 || seen[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses = %v, want one 201 and one 409", seen)
	}
	if n, err := h.store.AdminCount(t.Context()); err != nil || n != 1 {
		t.Fatalf("admin count = %d, %v; want exactly one", n, err)
	}
}
