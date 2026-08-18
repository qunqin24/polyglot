package pricing

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// firstParty lists the models.dev provider ids Polyglot takes prices from, in
// the order it prefers them.
//
// models.dev catalogues 188 providers, and most of them are resellers quoting
// their own margin: `claude-sonnet-4-5` appears under ten of them at three
// different prices, `gpt-5.5` under sixteen at four. There is no honest way to
// pick one reseller's number for another reseller's model, so Polyglot takes
// only the vendor that makes the model and calls that the official price. An
// operator buying the same model cheaper through a router corrects it by hand,
// which is the one thing an override is for.
//
// The order matters exactly once today — Z.ai lists GLM under both `zai` and
// `zhipuai` and the two disagree on `glm-5v-turbo` — but it is kept total so
// the answer can never depend on map iteration.
//
// This is a hand-kept list because it cannot be derived: the shape of a
// models.dev entry is identical for a vendor and for a reseller. Adding a
// vendor here is a one-line change plus a regenerated snapshot.
var firstParty = []string{
	"openai",
	"anthropic",
	"google",
	"xai",
	"deepseek",
	"alibaba", // Qwen
	"moonshotai",
	"zai", // Z.ai / GLM
	"zhipuai",
	"minimax",
	"mistral",
	"meta",
	"cohere",
	"perplexity",
	"stepfun",
	"xiaomi",
	"upstage",
	"sakana",
	"thinkingmachines",
	"poolside",
	"inception",
	"morph",
}

// tooGeneric are ids that name a routing behaviour rather than a model, so
// they cannot identify anything across vendors. Morph publishes `auto`, which
// after normalisation would claim the price of every other upstream that calls
// its automatic pick `auto` too.
var tooGeneric = map[string]bool{
	"auto":    true,
	"default": true,
	"chat":    true,
	"model":   true,
}

// Entry is one model's official price, already normalised to the key it is
// looked up by.
type Entry struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
	Rates
}

// Snapshot is the whole catalog plus the day it was taken. The version is what
// decides whether a freshly built binary's embedded copy should replace what a
// database already holds.
type Snapshot struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
}

// modelsDevFile is the part of https://models.dev/api.json this package reads.
// Everything else in that file — context limits, modalities, release dates —
// is deliberately left behind: it belongs to features that do not exist yet.
type modelsDevFile map[string]struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Cost *modelsDevCost `json:"cost"`
}

type modelsDevCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	// Tiers is the vendor's long-context pricing. Every first-party model that
	// has one has exactly one, keyed on context size, but the field is a list
	// and is read as one. models.dev also repeats the same numbers under
	// `context_over_200k`, which predates `tiers` and is not read: two spellings
	// of one fact, and the general one is the one that will still be there.
	Tiers []struct {
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
		Tier       struct {
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
}

// tier reads the long-context rung, if the vendor publishes one.
//
// Only a tier keyed on context size is understood. A tier keyed on something
// else would need a rule Polyglot does not have, and guessing at one would
// produce a confident wrong number — so it is skipped and the model is priced
// at its base rate, which is the answer for most requests anyway.
func (c modelsDevCost) tier() *Tier {
	for _, t := range c.Tiers {
		if t.Tier.Type != "context" || t.Tier.Size <= 0 {
			continue
		}
		return &Tier{
			AboveTokens: t.Tier.Size,
			Price: Price{
				Input:      t.Input,
				Output:     t.Output,
				CacheRead:  t.CacheRead,
				CacheWrite: t.CacheWrite,
			},
		}
	}
	return nil
}

// Parse trims a models.dev api.json body down to first-party prices.
//
// The full file is around 4 MB and 6,700 models; what survives is a few
// hundred entries, small enough to embed in the binary and to hold in memory
// without thinking about it.
func Parse(raw []byte, version string) (*Snapshot, error) {
	var file modelsDevFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse models.dev catalog: %w", err)
	}

	snap := &Snapshot{Version: version}
	seen := map[string]bool{}
	for _, vendor := range firstParty {
		p, ok := file[vendor]
		if !ok {
			continue
		}
		for id, m := range p.Models {
			// A model with no cost block is one models.dev has no price for.
			// Recording it as an entry with nothing in it would turn "unknown"
			// into "known to be blank" without adding an answer.
			if m.Cost == nil || (m.Cost.Input == nil && m.Cost.Output == nil) {
				continue
			}
			key := Normalize(id)
			if key == "" || seen[key] || tooGeneric[key] {
				continue
			}
			seen[key] = true
			snap.Entries = append(snap.Entries, Entry{
				ID:     key,
				Vendor: vendor,
				Rates: Rates{
					Price: Price{
						Input:      m.Cost.Input,
						Output:     m.Cost.Output,
						CacheRead:  m.Cost.CacheRead,
						CacheWrite: m.Cost.CacheWrite,
					},
					Tier: m.Cost.tier(),
				},
			})
		}
	}
	if len(snap.Entries) == 0 {
		return nil, fmt.Errorf("parse models.dev catalog: no first-party prices found")
	}
	// A stable order keeps the generated snapshot free of spurious diffs when
	// nothing about the prices changed.
	slices.SortFunc(snap.Entries, func(a, b Entry) int { return strings.Compare(a.ID, b.ID) })
	return snap, nil
}
