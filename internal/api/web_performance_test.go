package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/web"
)

func TestWebUIAndAdminCompression(t *testing.T) {
	h := newHarness(t, nil, "openai")
	admin := h.adminSession(t)
	get := func(path, encoding string) (http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Setting Accept-Encoding explicitly keeps net/http from automatically
		// decompressing, so assertions cover the actual bytes on the wire.
		req.Header.Set("Accept-Encoding", encoding)
		for _, cookie := range admin.cookies {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		return resp.Header, body
	}

	paths := []string{"/", "/models", "/index.html", "/api/providers"}
	if web.Built() {
		assets, err := web.FS()
		if err != nil {
			t.Fatal(err)
		}
		if err := fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && (strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css")) {
				paths = append(paths, "/"+path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var plainBytes, compressedBytes int
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			plainHeaders, plain := get(path, "identity")
			compressedHeaders, compressed := get(path, "gzip")
			if plainHeaders.Get("Content-Encoding") != "" || compressedHeaders.Get("Content-Encoding") != "gzip" {
				t.Fatal("content negotiation did not select the requested encoding")
			}
			if !strings.Contains(strings.Join(compressedHeaders.Values("Vary"), ","), "Accept-Encoding") {
				t.Fatal("compressed responses must vary by encoding")
			}
			reader, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(decoded, plain) {
				t.Fatalf("compression changed the response body: %v", err)
			}
			if strings.HasPrefix(path, "/assets/") {
				if compressedHeaders.Get("Cache-Control") != "public, max-age=31536000, immutable" {
					t.Fatal("fingerprinted assets lost their long browser cache")
				}
				plainBytes += len(plain)
				compressedBytes += len(compressed)
			} else if !strings.HasPrefix(path, "/api/") && compressedHeaders.Get("Cache-Control") != "no-cache" {
				t.Fatal("the app shell must revalidate after an upgrade")
			}
		})
	}
	if plainBytes > 0 {
		t.Logf("embedded JS/CSS transfer: %d -> %d bytes (%.1f%% smaller)", plainBytes, compressedBytes, 100*(1-float64(compressedBytes)/float64(plainBytes)))
		if compressedBytes >= plainBytes {
			t.Fatal("asset compression did not reduce transfer size")
		}
	}
}

func TestWebUICompressionLeavesGatewayStreamingUncompressed(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"model\":\"upstream-model-x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"model\":\"upstream-model-x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}, "openai")
	resp := h.post("/v1/chat/completions", `{"model":"my-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`, map[string]string{"Accept-Encoding": "gzip"})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("WebUI compression must not wrap the model gateway")
	}
	if !bytes.Contains(body, []byte("hello")) || !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Fatal("stream was not delivered in SSE format")
	}
}
