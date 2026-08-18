package api

import (
	"net/http"
	"testing"

	"github.com/qunqin24/polyglot/internal/update"
	"github.com/qunqin24/polyglot/internal/version"
)

func TestUpdateStatusRequiresAdminAndHonoursDisabledConfig(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")

	resp, err := http.Get(h.server.URL + "/api/update")
	if err != nil {
		t.Fatalf("anonymous update status: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	admin := h.adminSession(t)
	var got update.Status
	admin.get(t, "/api/update?refresh=true", &got)
	if got.Enabled || got.Supported || got.UpdateAvailable || got.CurrentVersion != version.Version {
		t.Fatalf("status = %+v", got)
	}
}
