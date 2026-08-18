package update

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStableBuildIgnoresPreviewTags(t *testing.T) {
	checker, requests := testChecker(t, "1.2.3", `[
		{"name":"v1.3.0-preview.2"}, {"name":"v1.2.4"}, {"name":"not-a-release"}
	]`)
	got := checker.Check(t.Context(), false)
	if !got.UpdateAvailable || got.LatestVersion != "1.2.4" || got.Channel != "stable" {
		t.Fatalf("status = %+v", got)
	}
	if got.LatestTag != "v1.2.4" || got.VersionURL != "https://github.com/owner/repo/tree/v1.2.4" {
		t.Errorf("release identity = %+v", got)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d", requests.Load())
	}
}

func TestPreviewBuildCanAdvanceToPreviewOrStable(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags string
		want string
	}{
		{name: "newer preview", tags: `[{"name":"v1.3.0-preview.2"},{"name":"v1.2.9"}]`, want: "1.3.0-preview.2"},
		{name: "stable graduation", tags: `[{"name":"v1.3.0-preview.2"},{"name":"v1.3.0"}]`, want: "1.3.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker, _ := testChecker(t, "v1.3.0-preview.1", tc.tags)
			got := checker.Check(t.Context(), false)
			if !got.UpdateAvailable || got.LatestVersion != tc.want || got.Channel != "preview" {
				t.Fatalf("status = %+v", got)
			}
		})
	}
}

func TestCurrentAndDevelopmentBuildsDoNotMisreport(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		checker, _ := testChecker(t, "1.2.3", `[{"name":"v1.2.3"},{"name":"v1.2.2"}]`)
		got := checker.Check(t.Context(), false)
		if got.UpdateAvailable || got.LatestVersion != "1.2.3" || got.Error != "" {
			t.Fatalf("status = %+v", got)
		}
	})
	t.Run("development", func(t *testing.T) {
		checker, requests := testChecker(t, "0.1.0-dev", `[{"name":"v9.0.0"}]`)
		got := checker.Check(t.Context(), false)
		if got.Supported || got.Channel != "development" || requests.Load() != 0 {
			t.Fatalf("status = %+v, requests = %d", got, requests.Load())
		}
	})
}

func TestCheckCachesSuccessAndForceRefreshes(t *testing.T) {
	checker, requests := testChecker(t, "1.0.0", `[{"name":"v1.0.1"}]`)
	checker.now = func() time.Time { return time.Unix(100, 0) }
	checker.Check(t.Context(), false)
	checker.Check(t.Context(), false)
	if requests.Load() != 1 {
		t.Fatalf("cached requests = %d", requests.Load())
	}
	checker.Check(t.Context(), true)
	if requests.Load() != 2 {
		t.Fatalf("forced requests = %d", requests.Load())
	}
}

func TestDisabledCheckerNeverCallsGitHub(t *testing.T) {
	checker, requests := testChecker(t, "1.0.0", `[{"name":"v2.0.0"}]`)
	checker.enabled = false
	got := checker.Check(t.Context(), true)
	if got.Enabled || requests.Load() != 0 {
		t.Fatalf("status = %+v, requests = %d", got, requests.Load())
	}
}

func testChecker(t *testing.T, current, response string) (*Checker, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Path != "/repos/owner/repo/tags" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("request URL = %s", r.URL.String())
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" ||
			r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Errorf("GitHub headers = %v", r.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    r,
		}, nil
	})}
	return newChecker("owner/repo", current, true, "https://api.example.test", client), &requests
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
