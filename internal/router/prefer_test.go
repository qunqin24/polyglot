package router_test

import (
	"testing"

	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/router"
)

func cand(name string, proto protocol.Name, priority int) router.Resolution {
	return router.Resolution{
		Target:   &provider.Target{Name: name, Protocol: proto},
		Priority: priority,
	}
}

func names(cands []router.Resolution) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Target.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ranked equally, the one needing no conversion wins: only a same-protocol
// route forwards a provider's own parameters and built-in tools.
func TestSameProtocolWinsATie(t *testing.T) {
	got := router.PreferProtocol([]router.Resolution{
		cand("openai-proxy", protocol.OpenAI, 0),
		cand("gemini-proxy", protocol.Gemini, 0),
		cand("claude-direct", protocol.Anthropic, 0),
	}, protocol.Anthropic)

	if want := []string{"claude-direct", "openai-proxy", "gemini-proxy"}; !equal(names(got), want) {
		t.Errorf("order = %v, want %v", names(got), want)
	}
}

// The one thing this must never do. Priority is something the operator set on
// purpose; a preference the gateway inferred does not get to overrule it.
func TestPriorityStillBeatsProtocol(t *testing.T) {
	got := router.PreferProtocol([]router.Resolution{
		// Higher priority, wrong protocol — the store already put it first.
		cand("openai-proxy", protocol.OpenAI, 10),
		cand("claude-direct", protocol.Anthropic, 0),
	}, protocol.Anthropic)

	if names(got)[0] != "openai-proxy" {
		t.Errorf("a protocol match overruled the operator's priority: %v", names(got))
	}
}

// Reordering happens inside a priority level, never across one.
func TestReorderingStaysWithinAPriorityLevel(t *testing.T) {
	got := router.PreferProtocol([]router.Resolution{
		cand("a-openai", protocol.OpenAI, 10),
		cand("b-anthropic", protocol.Anthropic, 10),
		cand("c-openai", protocol.OpenAI, 5),
		cand("d-anthropic", protocol.Anthropic, 5),
	}, protocol.Anthropic)

	want := []string{"b-anthropic", "a-openai", "d-anthropic", "c-openai"}
	if !equal(names(got), want) {
		t.Errorf("order = %v, want %v", names(got), want)
	}
}

// Every protocol converts to every other one, so a mismatch is a preference,
// not a disqualification. Dropping them would throw away the fallback chain.
func TestMismatchedProvidersAreKeptAsFallbacks(t *testing.T) {
	in := []router.Resolution{
		cand("openai-proxy", protocol.OpenAI, 0),
		cand("gemini-proxy", protocol.Gemini, 0),
	}
	got := router.PreferProtocol(in, protocol.Anthropic)

	if len(got) != 2 {
		t.Fatalf("a provider was filtered out: %v", names(got))
	}
	// With nothing to prefer, the store's order is untouched.
	if !equal(names(got), []string{"openai-proxy", "gemini-proxy"}) {
		t.Errorf("order changed with no native provider: %v", names(got))
	}
}

// Ties among equally-native providers keep the store's order, so the same
// request still lands in the same place every time.
func TestOrderStaysStableAmongEquals(t *testing.T) {
	in := []router.Resolution{
		cand("first", protocol.Anthropic, 0),
		cand("second", protocol.Anthropic, 0),
		cand("third", protocol.Anthropic, 0),
	}
	for range 5 {
		if !equal(names(router.PreferProtocol(in, protocol.Anthropic)),
			[]string{"first", "second", "third"}) {
			t.Fatal("equally native providers were reordered; routing is not deterministic")
		}
	}
}

func TestASingleCandidateIsUntouched(t *testing.T) {
	in := []router.Resolution{cand("only", protocol.Gemini, 0)}
	if got := router.PreferProtocol(in, protocol.Anthropic); len(got) != 1 || got[0].Target.Name != "only" {
		t.Errorf("a sole candidate was disturbed: %v", names(got))
	}
}
