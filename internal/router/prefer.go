package router

import (
	"sort"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// Preferring an upstream that speaks the client's own protocol.
//
// Every protocol converts to every other one through canonical, so this is
// never a filter: a Gemini upstream can serve an Anthropic client perfectly
// well. Removing the mismatched providers would throw away the fallback chain
// — one provider down and the request fails, with two healthy upstreams
// sitting unused.
//
// It is a preference, because a same-protocol route is measurably better. Only
// there do a provider's own parameters survive (`provider`, `guided_json`),
// only there are its built-in tools forwarded rather than reported, and only
// there does a reasoning signature travel without an envelope. Given two
// providers the operator ranked equally, the one needing no conversion is the
// better answer.
//
// What it must never do is outrank the operator. Priority is something they
// set deliberately — cheaper, more reliable, more quota — and a preference
// inferred by the gateway does not get to overrule a decision stated out loud.
// So this reorders only *within* a priority level, replacing a tiebreak that
// was previously arbitrary (provider id, i.e. the order they were created in).
func PreferProtocol(cands []Resolution, client protocol.Name) []Resolution {
	if len(cands) < 2 || client == "" {
		return cands
	}
	out := append([]Resolution(nil), cands...)

	// A stable sort on the priority key alone: entries the store already
	// ordered by priority keep that order, and only equals are rearranged.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return false // leave the store's ordering alone
		}
		iNative := out[i].Target.Protocol == client
		jNative := out[j].Target.Protocol == client
		return iNative && !jNative
	})
	return out
}
