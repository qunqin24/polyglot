// Package pricing turns the token counts on a request into money.
//
// Polyglot is a protocol gateway, not a billing system: nothing here deducts a
// balance, enforces a quota or blocks a request. The only job is to answer
// "roughly what did that cost", so an operator can see where their spend goes.
// Every number is an estimate and the UI says so.
//
// Prices come from two places, most specific first:
//
//  1. what the operator typed for that model
//  2. the official vendor price, from the models.dev catalog
//
// and when neither has an answer the cost is unknown — never zero. A zero
// reads as "this was free", which is a claim, and a claim nobody made.
package pricing

import (
	"math"
	"strings"
)

// Price is what a model costs, in US dollars per million tokens — the unit
// models.dev publishes, kept as-is so no conversion can drift.
//
// Every field is a pointer because "nobody stated this price" and "this price
// is zero" are different answers. A locally hosted model really is free and an
// operator can say so by setting 0; a model nothing knows about must stay
// blank instead of pretending to be free.
type Price struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

// Empty reports whether nothing at all is stated.
func (p Price) Empty() bool {
	return p.Input == nil && p.Output == nil && p.CacheRead == nil && p.CacheWrite == nil
}

// Overlay puts an operator's price on top of a catalog price, field by field.
//
// A blank field means "follow the catalog", which is why an override is stored
// as four nullable numbers rather than a copy of the catalog row: an operator
// who corrects only the input price still tracks an official output price cut.
func (p Price) Overlay(over Price) Price {
	out := p
	if over.Input != nil {
		out.Input = over.Input
	}
	if over.Output != nil {
		out.Output = over.Output
	}
	if over.CacheRead != nil {
		out.CacheRead = over.CacheRead
	}
	if over.CacheWrite != nil {
		out.CacheWrite = over.CacheWrite
	}
	return out
}

// Tier is what a model costs once a prompt passes a length. Several vendors
// charge more for a long context — OpenAI above 272k tokens, Google above
// 200k, xAI above 128k — and it is the whole schedule that changes, not just
// the input price, so a tier carries its own four numbers.
//
// Only the fields the vendor restates are set; the rest keep the base price.
type Tier struct {
	// AboveTokens is the prompt length the tier starts at, exclusive. It is
	// compared against the whole prompt, cached portion included, because that
	// is the context the model had to hold.
	AboveTokens int `json:"above_tokens"`
	Price
}

// Rates is a model's whole price schedule: the base price, and the tier above
// it for a vendor that charges more for a long prompt.
//
// An operator's override is a Price and never a Rates. They stated one set of
// numbers, and inventing a long-context multiple on top of it would be putting
// a price in their mouth — so an overridden model is charged flat.
type Rates struct {
	Price
	Tier *Tier `json:"tier,omitempty"`
}

// For returns the prices that apply to a prompt of this length, and whether
// the long-context tier is the one that answered.
func (r Rates) For(inputTokens int) (Price, bool) {
	if r.Tier != nil && inputTokens > r.Tier.AboveTokens {
		return r.Price.Overlay(r.Tier.Price), true
	}
	return r.Price, false
}

// Source names where a resolved price came from, so the UI never shows a
// number without saying who said it.
type Source string

const (
	SourceUnknown  Source = ""
	SourceCatalog  Source = "models.dev"
	SourceOverride Source = "custom"
)

// Tokens is what one request consumed, in the counts canonical.Usage defines:
// Input is the whole prompt, and CachedInput and CacheWrite are parts of it
// rather than additions to it.
type Tokens struct {
	Input       int
	CachedInput int
	CacheWrite  int
	Output      int
}

// Notes a cost can carry. They are recorded on the request log so a number
// that rests on an assumption says which one, rather than looking exact.
const (
	// NoteCacheAssumed says a cache price was missing and the plain input
	// price stood in for it.
	NoteCacheAssumed = "cache_price_assumed"
	// NoteLongContext says the prompt was long enough to be charged at the
	// vendor's higher long-context rate.
	NoteLongContext = "long_context_price"
)

// Cost is what a request came to.
type Cost struct {
	USD   float64
	Notes []string
}

// Compute prices one request. ok is false when the answer is not known well
// enough to state — no input or output price — and the caller must record an
// unknown cost rather than a zero.
//
// Reasoning tokens are deliberately absent: providers disagree about whether
// they are part of the output count or separate from it, so adding them would
// double count for some and under-report for others. The same reason the
// request log keeps them beside the totals instead of folding them in.
func Compute(r Rates, tk Tokens) (Cost, bool) {
	// Which rung of the schedule this prompt lands on. The comparison is
	// against the whole prompt, cached portion included: a vendor charging
	// more for a long context is charging for the context it had to hold, not
	// for the part it had to read afresh.
	p, longContext := r.For(tk.Input)
	if p.Input == nil || p.Output == nil {
		return Cost{}, false
	}

	var c Cost
	assumed := false

	// The prompt minus both cache portions: those are parts of the input
	// count, so charging the full input price on all of it would bill the
	// cached tokens twice.
	uncached := tk.Input - tk.CachedInput - tk.CacheWrite
	if uncached < 0 {
		// An upstream reported parts larger than the whole. Trust the parts,
		// which are the specific claims, and let the plain input go to zero.
		uncached = 0
	}
	c.USD += perMillion(uncached, *p.Input)

	if tk.CachedInput > 0 {
		rate := p.CacheRead
		if rate == nil {
			rate = p.Input
			assumed = true
		}
		c.USD += perMillion(tk.CachedInput, *rate)
	}
	if tk.CacheWrite > 0 {
		rate := p.CacheWrite
		if rate == nil {
			rate = p.Input
			assumed = true
		}
		c.USD += perMillion(tk.CacheWrite, *rate)
	}
	c.USD += perMillion(tk.Output, *p.Output)

	if longContext {
		c.Notes = append(c.Notes, NoteLongContext)
	}
	if assumed {
		c.Notes = append(c.Notes, NoteCacheAssumed)
	}
	return c, true
}

func perMillion(tokens int, price float64) float64 {
	if tokens <= 0 {
		return 0
	}
	return float64(tokens) / 1e6 * price
}

// Round trims a cost to a cent's worth of millionths. Floating point sums of
// per-token fractions accumulate noise well below anything worth showing, and
// a stored 0.0000000000000002 reads as a real, tiny charge.
func Round(usd float64) float64 {
	return math.Round(usd*1e8) / 1e8
}

// Normalize reduces a model id to the key the catalog is indexed by.
//
// The same model reaches Polyglot under several spellings: "claude-sonnet-4-5"
// direct from Anthropic, "anthropic/claude-sonnet-4-5" through a router,
// "models/gemini-2.5-pro" from a Gemini client, "deepseek-chat:free" with a
// routing suffix. They are one model at one official price, so the vendor
// prefix and the suffix come off and what is left is compared case-insensitively.
func Normalize(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}
