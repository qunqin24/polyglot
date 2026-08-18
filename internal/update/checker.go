// Package update checks Polyglot's public Git tags for a newer release.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIBase = "https://api.github.com"
	checkInterval  = 6 * time.Hour
	errorInterval  = 15 * time.Minute
	maxResponse    = 1 << 20
)

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-preview\.(\d+))?$`)

// Status is safe to return to the administrator UI. Error is deliberately a
// short classification; response bodies from a third party never cross into
// the browser.
type Status struct {
	Enabled         bool      `json:"enabled"`
	Supported       bool      `json:"supported"`
	CurrentVersion  string    `json:"current_version"`
	Channel         string    `json:"channel,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	LatestTag       string    `json:"latest_tag,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	VersionURL      string    `json:"version_url,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// Checker keeps one process-wide result so opening several browser tabs does
// not turn into several unauthenticated GitHub API requests.
type Checker struct {
	enabled  bool
	repo     string
	current  string
	apiBase  string
	client   *http.Client
	now      func() time.Time
	interval time.Duration

	mu        sync.Mutex
	cached    Status
	nextCheck time.Time
}

func New(repo, current string, enabled bool) *Checker {
	return newChecker(repo, current, enabled, defaultAPIBase, &http.Client{Timeout: 10 * time.Second})
}

func newChecker(repo, current string, enabled bool, apiBase string, client *http.Client) *Checker {
	return &Checker{
		enabled: enabled, repo: repo, current: current, apiBase: strings.TrimRight(apiBase, "/"),
		client: client, now: time.Now, interval: checkInterval,
	}
}

// Check returns a cached result unless force is true. Network errors are
// cached for a shorter window so an outage heals without hammering GitHub.
func (c *Checker) Check(ctx context.Context, force bool) Status {
	base := Status{Enabled: c.enabled, CurrentVersion: c.current}
	if !c.enabled {
		return base
	}
	current, ok := parseVersion(c.current)
	if !ok {
		base.Channel = "development"
		return base
	}
	base.Supported = true
	base.Channel = current.channel()

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if !force && !c.nextCheck.IsZero() && now.Before(c.nextCheck) {
		return c.cached
	}

	result := c.fetch(ctx, current, base, now)
	c.cached = result
	if result.Error == "" {
		c.nextCheck = now.Add(c.interval)
	} else {
		c.nextCheck = now.Add(errorInterval)
	}
	return result
}

func (c *Checker) fetch(ctx context.Context, current semVersion, status Status, now time.Time) Status {
	endpoint := c.apiBase + "/repos/" + c.repo + "/tags?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.Error = "build update request"
		status.CheckedAt = now
		return status
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Polyglot/"+c.current)

	resp, err := c.client.Do(req)
	if err != nil {
		status.Error = "reach GitHub"
		status.CheckedAt = now
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		status.Error = fmt.Sprintf("GitHub returned %d", resp.StatusCode)
		status.CheckedAt = now
		return status
	}

	var tags []struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponse))
	if err := dec.Decode(&tags); err != nil {
		status.Error = "decode GitHub response"
		status.CheckedAt = now
		return status
	}

	var latest *semVersion
	latestTag := ""
	for _, tag := range tags {
		candidate, ok := parseVersion(tag.Name)
		if !ok || (current.preview == nil && candidate.preview != nil) {
			continue
		}
		if latest == nil || candidate.compare(*latest) > 0 {
			v := candidate
			latest = &v
			latestTag = tag.Name
		}
	}
	status.CheckedAt = now
	if latest == nil {
		status.Error = "no compatible release tags"
		return status
	}
	status.LatestVersion = latest.String()
	status.LatestTag = latestTag
	status.UpdateAvailable = latest.compare(current) > 0
	status.VersionURL = "https://github.com/" + c.repo + "/tree/" + url.PathEscape(latestTag)
	return status
}

type semVersion struct {
	major   int
	minor   int
	patch   int
	preview *int
}

func parseVersion(raw string) (semVersion, bool) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return semVersion{}, false
	}
	parts := [3]int{}
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semVersion{}, false
		}
		parts[i] = n
	}
	v := semVersion{major: parts[0], minor: parts[1], patch: parts[2]}
	if m[4] != "" {
		n, err := strconv.Atoi(m[4])
		if err != nil {
			return semVersion{}, false
		}
		v.preview = &n
	}
	return v, true
}

func (v semVersion) compare(other semVersion) int {
	for _, pair := range [][2]int{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if v.preview == nil && other.preview != nil {
		return 1
	}
	if v.preview != nil && other.preview == nil {
		return -1
	}
	if v.preview == nil {
		return 0
	}
	if *v.preview < *other.preview {
		return -1
	}
	if *v.preview > *other.preview {
		return 1
	}
	return 0
}

func (v semVersion) channel() string {
	if v.preview != nil {
		return "preview"
	}
	return "stable"
}

func (v semVersion) String() string {
	base := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if v.preview != nil {
		return fmt.Sprintf("%s-preview.%d", base, *v.preview)
	}
	return base
}
