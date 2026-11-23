package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// The Overview's spend report counts a request with no known price as
// unpriced, never as free, and arrays are never null.
func TestSpendReportCountsUnpricedRequests(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")
	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	h.waitForLog(t)

	admin := h.adminSession(t)
	for _, rng := range []string{"24h", "7d", "30d", "month"} {
		for _, dim := range []string{"model", "provider", "key"} {
			var raw map[string]json.RawMessage
			admin.get(t, "/api/stats/spend?range="+rng+"&dim="+dim, &raw)
			for _, f := range []string{"starts", "stacks", "rows", "names"} {
				if string(raw[f]) == "null" || raw[f] == nil {
					t.Errorf("%s/%s: .%s is %s, want an array or object", rng, dim, f, raw[f])
				}
			}
		}
	}

	var got struct {
		Range            string  `json:"range"`
		CostUSD          float64 `json:"cost_usd"`
		Requests         int64   `json:"requests"`
		UnpricedRequests int64   `json:"unpriced_requests"`
		From             int64   `json:"from"`
		PeriodEnd        int64   `json:"period_end"`
		Rows             []struct {
			Key          string `json:"key"`
			ProviderName string `json:"provider_name"`
			Unpriced     int64  `json:"unpriced"`
		} `json:"rows"`
	}
	admin.get(t, "/api/stats/spend?range=month&dim=model", &got)
	if got.Range != "month" || got.Requests != 1 || got.UnpricedRequests != 1 || got.CostUSD != 0 {
		t.Errorf("report = %+v, want one unpriced request this month", got)
	}
	if len(got.Rows) != 1 || got.Rows[0].Key != "upstream-model-x" || got.Rows[0].ProviderName != "fake" || got.Rows[0].Unpriced != 1 {
		t.Errorf("rows = %+v", got.Rows)
	}
	if got.PeriodEnd <= got.From || time.Unix(got.PeriodEnd, 0).Sub(time.Unix(got.From, 0)) > 31*24*time.Hour {
		t.Errorf("month runs %v to %v", time.Unix(got.From, 0), time.Unix(got.PeriodEnd, 0))
	}

	resp, err := http.Get(h.server.URL + "/api/stats/spend")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", resp.StatusCode)
	}
}
