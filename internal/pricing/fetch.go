package pricing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CatalogURL is the published models.dev catalog.
//
// This is the only address Polyglot dials that no operator configured, and it
// is dialled only when someone presses refresh. Nothing is sent: it is a GET
// of a public file, with no identifier, no query and no body. The binary ships
// with a snapshot precisely so a gateway that never wants outbound traffic
// still has prices.
const CatalogURL = "https://models.dev/api.json"

// The published file is around 4 MB. The cap is generous enough to survive it
// growing and small enough that a wrong URL cannot fill memory.
const maxCatalogBytes = 64 << 20

var catalogClient = &http.Client{Timeout: 60 * time.Second}

// Fetch downloads and trims the current catalog.
//
// Every failure here is information, never something that breaks the gateway:
// the caller keeps the catalog it already had and shows the operator what went
// wrong.
func Fetch(ctx context.Context) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CatalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch price catalog: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := catalogClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch price catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch price catalog: models.dev returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch price catalog: %w", err)
	}
	if len(raw) > maxCatalogBytes {
		return nil, fmt.Errorf("fetch price catalog: response over %d bytes", maxCatalogBytes)
	}
	return Parse(raw, time.Now().UTC().Format("2006-01-02"))
}
