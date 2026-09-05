package capture

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBytesSurviveSplitUTF8AndMetadataDropsCredentials(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Start(Stage{ID: "request", Headers: http.Header{"Authorization": {"Bearer secret"}, "X-Custom-Secret": {"private"}, "Content-Type": {"application/json"}}, URL: "https://user:pass@example.com/v1?key=secret&alt=sse"})
	want := []byte("真实提示词 🔍\x00")
	for _, b := range want {
		r.Data("request", []byte{b})
	}
	r.End("request")
	w := httptest.NewRecorder()
	wrapped := r.Writer(w)
	if _, ok := wrapped.(http.Flusher); !ok {
		t.Fatal("lost Flusher")
	}
	wrapped.Write([]byte("reply"))
	FinishResponse(wrapped)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, r.ID+".jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []byte
	if err := Walk(f, func(e Event) error {
		if e.Type == "start" && e.Stage.ID == "request" {
			if e.Stage.Headers.Get("Authorization") != "" || e.Stage.Headers.Get("X-Custom-Secret") != "" {
				t.Fatal("secret header retained")
			}
			if bytes.Contains([]byte(e.Stage.URL), []byte("secret")) || bytes.Contains([]byte(e.Stage.URL), []byte("pass")) {
				t.Fatal("URL credential retained")
			}
		}
		if e.ID == "request" {
			got = append(got, e.Data...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes changed: %q", got)
	}
	f.Seek(0, io.SeekStart)
	m, err := ReadManifest(f)
	if err != nil || !m.Complete || len(m.Stages) != 2 || !m.Stages[1].Complete {
		t.Fatalf("manifest=%+v err=%v", m, err)
	}
}
