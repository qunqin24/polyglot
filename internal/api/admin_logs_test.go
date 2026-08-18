package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

func TestLogsSupportOffsetPaginationAndTotal(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}, "openai")

	now := time.Now()
	rows := make([]*store.RequestLog, 0, 5)
	for i := range 5 {
		rows = append(rows, &store.RequestLog{
			StartedAt: now.Add(time.Duration(i) * time.Second), FinishedAt: now,
			Status: "success", StatusCode: http.StatusOK, ClientProtocol: "openai",
			ModelAlias: "pagination-model",
		})
	}
	if err := h.store.InsertRequestLogs(context.Background(), rows); err != nil {
		t.Fatal(err)
	}

	admin := h.adminSession(t)
	var page struct {
		Logs    []*store.RequestLog `json:"logs"`
		HasMore bool                `json:"has_more"`
		Total   int64               `json:"total"`
	}
	admin.get(t, "/api/logs?model=pagination-model&limit=2&offset=2", &page)
	if page.Total != 5 || len(page.Logs) != 2 || !page.HasMore {
		t.Fatalf("middle page = total %d, rows %d, has_more %v", page.Total, len(page.Logs), page.HasMore)
	}

	admin.get(t, "/api/logs?model=pagination-model&limit=2&offset=4", &page)
	if page.Total != 5 || len(page.Logs) != 1 || page.HasMore {
		t.Fatalf("last page = total %d, rows %d, has_more %v", page.Total, len(page.Logs), page.HasMore)
	}
}
