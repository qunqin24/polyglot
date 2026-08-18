// Package compatibility checks that the official vendor SDKs can use Polyglot
// as if it were the vendor's own API.
//
// These tests deliberately import nothing from Polyglot. They build the real
// binary, run it as a process, and drive it over HTTP:
//
//	Official SDK -> HTTP -> Polyglot (real binary) -> HTTP -> mock upstream
//
// That is the whole point: request serialisation, headers, URLs, status codes,
// JSON shapes, SSE framing, stream termination and error bodies are all things
// an SDK is strict about and a codec unit test cannot observe. Calling a codec
// function directly would prove nothing about any of them.
//
// The upstream is a local mock, so the suite never needs a paid API key.
package compatibility

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Provider names, used as the `provider::model` prefix to pick which upstream
// protocol a request is converted into.
const (
	upOpenAI    = "up-openai"
	upResponses = "up-responses"
	upAnthropic = "up-anthropic"
	upGemini    = "up-gemini"
	setupToken  = "compatibility-setup-token"
)

var gw *gateway

// gateway is a running Polyglot process plus the credentials to call it.
type gateway struct {
	baseURL string
	apiKey  string
	// upstream records what the mock last received, so a test can assert on
	// what Polyglot actually sent rather than only on what came back.
	upstream *mockUpstream
}

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compatibility harness: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "polyglot-compat-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "polyglot")
	build := exec.Command("go", "build", "-o", bin, "./cmd/polyglot")
	build.Dir = "../.."
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return 0, fmt.Errorf("build polyglot: %w", err)
	}

	up := newMockUpstream()
	srv := httptest.NewServer(up)
	defer srv.Close()

	port, err := freePort()
	if err != nil {
		return 0, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	proc := exec.Command(bin)
	proc.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATA_DIR="+filepath.Join(dir, "data"),
		"POLYGLOT_SETUP_TOKEN="+setupToken,
	)
	proc.Stdout = io.Discard
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		return 0, fmt.Errorf("start polyglot: %w", err)
	}
	defer func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	}()

	if err := waitHealthy(base); err != nil {
		return 0, err
	}
	key, err := seed(base, srv.URL)
	if err != nil {
		return 0, err
	}

	gw = &gateway{baseURL: base, apiKey: key, upstream: up}
	return m.Run(), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitHealthy(base string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("polyglot did not become healthy at %s", base)
}

// seed walks the same first-run flow an operator would: create the admin, log
// in, register the upstreams, mint an API key.
func seed(base, upstreamURL string) (string, error) {
	c := &adminClient{base: base, http: &http.Client{Timeout: 20 * time.Second}, setupToken: setupToken}

	if err := c.do("POST", "/api/setup", map[string]any{
		"username": "admin", "password": "compat-test-password",
	}, nil); err != nil {
		return "", fmt.Errorf("setup: %w", err)
	}
	if err := c.login("admin", "compat-test-password"); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}

	// One provider per upstream protocol, all pointing at the same mock, which
	// tells them apart by path. Tests select one with `provider::model`.
	for name, proto := range map[string]string{
		upOpenAI:    "openai",
		upResponses: "openai-responses",
		upAnthropic: "anthropic",
		upGemini:    "gemini",
	} {
		// Models are registered explicitly, exactly as an operator picks them in
		// the dialog. Nothing lands in the registry on its own.
		if err := c.do("POST", "/api/providers", map[string]any{
			"name": name, "protocol": proto, "base_url": upstreamURL,
			"api_key": "sk-mock-upstream",
			"models":  []map[string]string{{"id": "mock-model"}},
		}, nil); err != nil {
			return "", fmt.Errorf("create provider %s: %w", name, err)
		}
	}

	var out struct {
		Secret string `json:"secret"`
	}
	if err := c.do("POST", "/api/keys", map[string]any{"name": "compat"}, &out); err != nil {
		return "", fmt.Errorf("create key: %w", err)
	}
	if out.Secret == "" {
		return "", fmt.Errorf("key creation returned no secret")
	}
	return out.Secret, nil
}

// adminClient speaks the admin API the way the WebUI does: a session cookie
// plus the double-submit CSRF token on every state-changing request.
type adminClient struct {
	base       string
	http       *http.Client
	cookies    []*http.Cookie
	csrf       string
	setupToken string
}

func (c *adminClient) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if path == "/api/setup" && c.setupToken != "" {
		req.Header.Set("X-Polyglot-Setup-Token", c.setupToken)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *adminClient) login(user, pass string) error {
	b, _ := json.Marshal(map[string]any{"username": user, "password": pass})
	resp, err := c.http.Post(c.base+"/api/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("login: %d %s", resp.StatusCode, body)
	}
	c.cookies = resp.Cookies()
	for _, ck := range c.cookies {
		if ck.Name == "polyglot_csrf" {
			c.csrf = ck.Value
		}
	}
	if c.csrf == "" {
		return fmt.Errorf("login did not set a CSRF cookie")
	}
	return nil
}
