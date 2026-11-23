package live

import "github.com/qunqin24/polyglot/internal/store"

// FromLog describes a request log row as a live request. It is used twice: at
// the first attempt, when the row holds only what routing has decided so far,
// and at the end, when the usage logger has priced it — so the final event and
// the log row can never disagree.
//
// A row that has not finished (FinishedAt zero) is running. Cost stays nil
// when the row has none: unknown is not free.
func FromLog(rec *store.RequestLog) Request {
	r := Request{
		ID:               rec.RequestID,
		StartedAt:        rec.StartedAt.UnixMilli(),
		Model:            rec.UpstreamModel,
		Provider:         rec.ProviderName,
		APIKey:           rec.APIKeyName,
		ClientProtocol:   rec.ClientProtocol,
		UpstreamProtocol: rec.UpstreamProtocol,
		Stream:           rec.Stream,
		Status:           StatusRunning,
	}
	if rec.ProviderID != nil {
		r.ProviderID = *rec.ProviderID
	}
	if rec.FinishedAt.IsZero() {
		return r
	}
	ended := rec.FinishedAt.UnixMilli()
	r.EndedAt = &ended
	if rec.TTFTMS != nil {
		ft := rec.StartedAt.UnixMilli() + *rec.TTFTMS
		r.FirstTokenAt = &ft
	}
	// "cancelled" is a client hanging up. The dashboard has two end states, and
	// that is not a success, so it is shown as an error with its real code
	// (499) and error_type (client_disconnect).
	r.Status = StatusError
	if rec.Status == "success" {
		r.Status = StatusOK
	}
	r.StatusCode = rec.StatusCode
	r.ErrorType = rec.ErrorType
	r.InputTokens = rec.InputTokens
	r.OutputTokens = rec.OutputTokens
	r.CostUSD = rec.CostUSD
	return r
}
