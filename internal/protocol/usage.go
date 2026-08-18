package protocol

import "github.com/qunqin24/polyglot/internal/canonical"

// NoteCacheWrite reports a cache-creation count the target protocol cannot
// express.
//
// Anthropic is the only one of the five that bills writing to a prompt cache
// separately from reading back out of it, and so the only one with a field for
// the count. The other four report a prompt total with the cached read called
// out inside it and say nothing about writes.
//
// The tokens are not lost: canonical.Usage.InputTokens counts them, so the
// prompt total a client receives is correct. What is lost is the breakdown —
// and an operator reconciling a bill needs to know the number was folded in
// rather than that there was never one, because cache writes are charged at a
// different rate from ordinary input.
//
// A codec whose protocol has no cache-write field calls this from
// EncodeResponse. Anthropic's does not: it writes the field instead.
//
// Streaming has no equivalent. A stream encoder is handed no Diagnostics, so a
// streamed reply carries the same folded total with no note — the same gap
// that loses response extensions on a streamed route.
// NoteTextSignatures reports replay tokens the target protocol cannot carry.
//
// Gemini 3 closes a thinking block with a thoughtSignature on the part that
// follows it, which for a plain answer is the text. Tool calls have Google's
// own extra_content envelope to travel in; a text part has none, and inventing
// a spelling for one is exactly what this project must not do.
//
// So on a route to any other protocol the token is dropped — and said out
// loud, because the consequence surfaces a turn later and somewhere else: the
// history comes back unsigned and the reasoning behind it can no longer be
// replayed to Gemini.
func NoteTextSignatures(req *canonical.Request, d *canonical.Diagnostics) {
	n := 0
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Type == canonical.PartText && p.Signature != "" {
				n++
			}
		}
	}
	if n == 0 {
		return
	}
	d.Note("messages.signature", canonical.FidelityLossy,
		"%d reasoning replay token(s) were dropped: this protocol has no field for one on a text part, "+
			"so a later turn cannot replay the reasoning they belong to", n)
}

// NoteCacheHints reports prompt-cache breakpoints the target protocol has no
// way to express.
//
// Anthropic is the only one with a marker a client can place; the others cache
// automatically, or not at all, with nothing for a caller to ask for. Dropping
// the marker does not break the request — it just quietly returns the caller to
// the uncached rate, which is exactly the kind of loss that has to be said out
// loud rather than discovered on an invoice.
//
// A codec whose protocol has no breakpoint marker calls this from
// EncodeRequest. Anthropic's does not: it writes them.
func NoteCacheHints(req *canonical.Request, d *canonical.Diagnostics) {
	n := 0
	for _, p := range req.System {
		if p.Cache != nil {
			n++
		}
	}
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Cache != nil {
				n++
			}
		}
	}
	for _, t := range req.Tools {
		if t.Cache != nil {
			n++
		}
	}
	if n == 0 {
		return
	}
	d.Note("cache_control", canonical.FidelityUnsupported,
		"%d prompt-cache breakpoint(s) were dropped: this protocol has no marker for one, "+
			"so the prompt is sent uncached", n)
}

func NoteCacheWrite(u canonical.Usage, d *canonical.Diagnostics) {
	if u.CacheWriteTokens <= 0 {
		return
	}
	d.Note("usage.cache_write_tokens", canonical.FidelityLossy,
		"%d tokens written to the upstream's prompt cache are included in the input total; "+
			"this protocol has no separate field for them, and they are billed at a different rate",
		u.CacheWriteTokens)
}
