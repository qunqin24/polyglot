package pricing

import (
	"context"
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

// The prompt cache is the whole reason this arithmetic is not a multiplication.
// Both cache counts are parts of the input total, so charging the full input
// price on the whole prompt bills the cached tokens twice — at the moment the
// cache was meant to make them ten times cheaper.
func TestCachedTokensAreChargedAtTheCachePriceNotTheInputPrice(t *testing.T) {
	price := Price{Input: f(3), Output: f(15), CacheRead: f(0.3), CacheWrite: f(3.75)}
	// 5,340 prompt tokens: 40 fresh, 5,000 read from cache, 300 written to it.
	tk := Tokens{Input: 5340, CachedInput: 5000, CacheWrite: 300, Output: 1000}

	got, ok := Compute(Rates{Price: price}, tk)
	if !ok {
		t.Fatal("a model with published prices produced no cost")
	}
	want := 40*3/1e6 + 5000*0.3/1e6 + 300*3.75/1e6 + 1000*15/1e6
	if math.Abs(got.USD-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", got.USD, want)
	}
	if len(got.Notes) != 0 {
		t.Errorf("notes = %v on a fully priced model", got.Notes)
	}

	// The bug this guards: treating the parts as additions to the prompt.
	naive := 5340*3/1e6 + 5000*0.3/1e6 + 300*3.75/1e6 + 1000*15/1e6
	if math.Abs(got.USD-naive) < 1e-12 {
		t.Error("the cached tokens were charged twice: once at the input price and once at the cache price")
	}
}

// Two thirds of the catalog publishes no cache price. Falling back to the input
// price is the closest honest answer, but it is an assumption, and an estimate
// that looks exact is worse than one that says what it stands on.
func TestAMissingCachePriceIsAssumedOutLoud(t *testing.T) {
	price := Price{Input: f(1), Output: f(2)}
	got, ok := Compute(Rates{Price: price}, Tokens{Input: 1000, CachedInput: 900, Output: 10})
	if !ok {
		t.Fatal("no cost from a model with input and output prices")
	}
	// Every prompt token at the input price, cached or not.
	if want := 1000*1.0/1e6 + 10*2.0/1e6; math.Abs(got.USD-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", got.USD, want)
	}
	if len(got.Notes) != 1 || got.Notes[0] != NoteCacheAssumed {
		t.Errorf("notes = %v, want the cache assumption recorded", got.Notes)
	}
}

// No note when nothing was assumed: a request that never touched the cache is
// exactly priced even though the model has no cache price.
func TestNoCacheTokensMeansNoAssumption(t *testing.T) {
	got, ok := Compute(Rates{Price: Price{Input: f(1), Output: f(2)}}, Tokens{Input: 100, Output: 10})
	if !ok || len(got.Notes) != 0 {
		t.Errorf("ok = %v, notes = %v; an uncached request assumed nothing", ok, got.Notes)
	}
}

// A model nobody has a price for costs an unknown amount. Zero is a claim that
// the request was free, and nobody made that claim.
func TestAModelWithNoPriceCostsAnUnknownAmountNotZero(t *testing.T) {
	if _, ok := Compute(Rates{}, Tokens{Input: 1000, Output: 100}); ok {
		t.Error("a model with no price produced a cost")
	}
	// Half a price is still no price: charging for output while pretending
	// input was free would be worse than saying nothing.
	if _, ok := Compute(Rates{Price: Price{Output: f(15)}}, Tokens{Input: 1000, Output: 100}); ok {
		t.Error("a model with only an output price produced a cost")
	}
	// A stated zero is a different answer and does produce one.
	if c, ok := Compute(Rates{Price: Price{Input: f(0), Output: f(0)}}, Tokens{Input: 1000, Output: 100}); !ok || c.USD != 0 {
		t.Errorf("a model priced at zero should cost zero: ok=%v cost=%v", ok, c.USD)
	}
}

// An upstream that reports parts larger than the whole must not produce a
// negative charge that quietly cancels out other requests in a total.
func TestPartsLargerThanTheWholePromptNeverGoNegative(t *testing.T) {
	got, ok := Compute(Rates{Price: Price{Input: f(3), Output: f(15), CacheRead: f(0.3)}},
		Tokens{Input: 100, CachedInput: 5000, Output: 0})
	if !ok {
		t.Fatal("no cost")
	}
	if got.USD < 0 {
		t.Errorf("cost = %v, which is negative", got.USD)
	}
}

// gpt-5.5 doubles above 272k tokens, gemini-2.5-pro rises above 200k. A long
// prompt is also the expensive kind of request, so pricing one at the base rate
// gets the largest amounts most wrong.
func TestALongPromptIsChargedAtTheLongContextRate(t *testing.T) {
	rates := Rates{
		Price: Price{Input: f(5), Output: f(30), CacheRead: f(0.5)},
		Tier: &Tier{AboveTokens: 272_000, Price: Price{
			Input: f(10), Output: f(45), CacheRead: f(1),
		}},
	}

	// Just under the threshold: the base schedule, and no note.
	short, ok := Compute(rates, Tokens{Input: 272_000, Output: 1000})
	if !ok {
		t.Fatal("no cost under the threshold")
	}
	if want := 272_000*5/1e6 + 1000*30/1e6; math.Abs(short.USD-want) > 1e-9 {
		t.Errorf("cost under the threshold = %v, want %v", short.USD, want)
	}
	if len(short.Notes) != 0 {
		t.Errorf("notes = %v on a prompt that never reached the tier", short.Notes)
	}

	// One token over: the whole schedule changes, output included.
	long, ok := Compute(rates, Tokens{Input: 272_001, Output: 1000})
	if !ok {
		t.Fatal("no cost over the threshold")
	}
	if want := 272_001*10/1e6 + 1000*45/1e6; math.Abs(long.USD-want) > 1e-9 {
		t.Errorf("cost over the threshold = %v, want %v — the output price rises too", long.USD, want)
	}
	if len(long.Notes) != 1 || long.Notes[0] != NoteLongContext {
		t.Errorf("notes = %v, want the long-context rate recorded", long.Notes)
	}
}

// The threshold is the whole prompt, cached part included: a vendor charging
// more for a long context is charging for the context it had to hold, not for
// the part it had to read afresh.
func TestTheTierThresholdCountsTheCachedPromptToo(t *testing.T) {
	rates := Rates{
		Price: Price{Input: f(5), Output: f(30), CacheRead: f(0.5)},
		Tier: &Tier{AboveTokens: 200_000, Price: Price{
			Input: f(10), Output: f(45), CacheRead: f(1),
		}},
	}
	// 300k prompt, almost all of it served from the cache.
	got, ok := Compute(rates, Tokens{Input: 300_000, CachedInput: 290_000, Output: 100})
	if !ok {
		t.Fatal("no cost")
	}
	want := 10_000*10/1e6 + 290_000*1/1e6 + 100*45/1e6
	if math.Abs(got.USD-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", got.USD, want)
	}
	if len(got.Notes) != 1 || got.Notes[0] != NoteLongContext {
		t.Errorf("notes = %v, want the long-context rate recorded", got.Notes)
	}
}

// A tier restates only some prices. The ones it leaves out keep the base
// number rather than becoming unset, which would price the cache at nothing.
func TestATierKeepsTheBasePriceForWhatItDoesNotRestate(t *testing.T) {
	rates := Rates{
		Price: Price{Input: f(5), Output: f(30), CacheWrite: f(6.25)},
		Tier:  &Tier{AboveTokens: 100, Price: Price{Input: f(10), Output: f(45)}},
	}
	got, _ := rates.For(1000)
	if got.CacheWrite == nil || *got.CacheWrite != 6.25 {
		t.Errorf("cache write = %v above the threshold, want the base 6.25 the tier did not restate",
			got.CacheWrite)
	}
}

// models.dev spells the same fact twice: `tiers` and the older
// `context_over_200k`. Reading both would be a chance to disagree with itself.
func TestALongContextTierIsReadFromTheCatalog(t *testing.T) {
	raw := []byte(`{"openai": {"models": {"gpt-5.5": {"cost": {
		"input": 5, "output": 30, "cache_read": 0.5,
		"tiers": [{"input": 10, "output": 45, "cache_read": 1,
		           "tier": {"type": "context", "size": 272000}}],
		"context_over_200k": {"input": 99, "output": 99}
	}}}}}`)
	snap, err := Parse(raw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := snap.Entries[0]
	if e.Tier == nil {
		t.Fatal("the long-context tier was dropped")
	}
	if e.Tier.AboveTokens != 272_000 || e.Tier.Input == nil || *e.Tier.Input != 10 {
		t.Errorf("tier = %+v, want 272k / 10", e.Tier)
	}
}

// A tier keyed on anything but context size needs a rule Polyglot does not
// have. Skipping it prices the model at its base rate, which is the answer for
// most requests; guessing would produce a confident wrong number.
func TestATierKeyedOnSomethingElseIsIgnored(t *testing.T) {
	raw := []byte(`{"openai": {"models": {"gpt-x": {"cost": {
		"input": 5, "output": 30,
		"tiers": [{"input": 50, "output": 90, "tier": {"type": "priority", "size": 1}}]
	}}}}}`)
	snap, err := Parse(raw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Entries[0].Tier != nil {
		t.Errorf("tier = %+v, want none: its rule is not one we know", snap.Entries[0].Tier)
	}
}

// The shipped catalog has to actually carry the tiers, or the feature is only
// true in tests.
func TestTheEmbeddedCatalogCarriesLongContextTiers(t *testing.T) {
	snap, err := Embedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	tiered := 0
	for _, e := range snap.Entries {
		if e.Tier == nil {
			continue
		}
		tiered++
		if e.Tier.AboveTokens <= 0 {
			t.Errorf("%s has a tier with no threshold", e.ID)
		}
		if e.Tier.Input == nil && e.Tier.Output == nil {
			t.Errorf("%s has a tier that restates no price", e.ID)
		}
	}
	if tiered == 0 {
		t.Error("no model in the shipped catalog has a long-context tier")
	}
}

// The same model arrives spelled several ways: bare from the vendor, prefixed
// through a router, path-style from a Gemini client, suffixed with a routing
// hint. One model, one official price.
func TestNormalizeReducesEverySpellingToOneKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"Anthropic/Claude-Sonnet-4-5", "claude-sonnet-4-5"},
		{"models/gemini-2.5-pro", "gemini-2.5-pro"},
		{"deepseek/deepseek-chat:free", "deepseek-chat"},
		{"  gpt-5.5  ", "gpt-5.5"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// models.dev catalogues resellers alongside vendors, and they disagree: one
// model id appears under a dozen providers at several prices. Taking whichever
// one turns up first would make the official price a coin toss, so only the
// vendor that makes the model is ingested.
func TestOnlyTheVendorsOwnPriceIsIngested(t *testing.T) {
	raw := []byte(`{
		"anthropic": {"models": {"claude-sonnet-4-5": {"cost": {"input": 3, "output": 15}}}},
		"a-reseller": {"models": {"anthropic/claude-sonnet-4-5": {"cost": {"input": 3.75, "output": 18.75}}}},
		"another-reseller": {"models": {"claude-sonnet-4-5": {"cost": {"input": 9, "output": 45}}}}
	}`)
	snap, err := Parse(raw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %+v, want only the vendor's", snap.Entries)
	}
	e := snap.Entries[0]
	if e.Vendor != "anthropic" || e.Input == nil || *e.Input != 3 {
		t.Errorf("entry = %+v, want anthropic's own 3 / 15", e)
	}
}

// Morph publishes a model called "auto". Normalised, it would claim the price
// of every other upstream that names its automatic pick the same way.
func TestAnIdThatNamesNoModelIsNotAPrice(t *testing.T) {
	raw := []byte(`{"morph": {"models": {
		"auto":        {"cost": {"input": 0.85, "output": 1.55}},
		"morph-v3-fast": {"cost": {"input": 0.8, "output": 1.2}}
	}}}`)
	snap, err := Parse(raw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, e := range snap.Entries {
		if e.ID == "auto" {
			t.Error(`"auto" was ingested as a model price`)
		}
	}
}

// A model models.dev lists but has no price for must stay unknown rather than
// becoming an entry with nothing in it, which would read as a known blank.
func TestAModelWithNoCostBlockIsNotAnEntry(t *testing.T) {
	raw := []byte(`{"google": {"models": {
		"gemma-free":   {},
		"gemini-x":     {"cost": {"input": 1, "output": 2}}
	}}}`)
	snap, err := Parse(raw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Entries) != 1 || snap.Entries[0].ID != "gemini-x" {
		t.Errorf("entries = %+v, want only the model with a published cost", snap.Entries)
	}
}

// The shipped snapshot has to be usable, not merely parseable: a build whose
// catalog is empty or malformed prices nothing and nobody would notice until
// the WebUI showed dashes everywhere.
func TestTheEmbeddedCatalogIsUsable(t *testing.T) {
	snap, err := Embedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	if snap.Version == "" || len(snap.Entries) < 50 {
		t.Fatalf("embedded catalog looks wrong: version %q, %d entries", snap.Version, len(snap.Entries))
	}
	for _, e := range snap.Entries {
		if e.ID != Normalize(e.ID) {
			t.Errorf("entry %q is not stored under its normalised id", e.ID)
		}
		if e.Input == nil && e.Output == nil {
			t.Errorf("entry %q carries no price at all", e.ID)
		}
	}
}

// An override is four nullable numbers rather than a copy of the catalog row,
// so correcting one price still tracks an official cut in the others.
func TestAnOverrideReplacesOnlyWhatWasTyped(t *testing.T) {
	catalog := Price{Input: f(3), Output: f(15), CacheRead: f(0.3)}
	got := catalog.Overlay(Price{Input: f(1.5)})

	if got.Input == nil || *got.Input != 1.5 {
		t.Errorf("input = %v, want the operator's 1.5", got.Input)
	}
	if got.Output == nil || *got.Output != 15 {
		t.Errorf("output = %v, want the catalog's 15 — the operator did not set one", got.Output)
	}
	if got.CacheRead == nil || *got.CacheRead != 0.3 {
		t.Errorf("cache read = %v, want the catalog's 0.3", got.CacheRead)
	}
}

// fakeStore is the pricing data without a database behind it.
type fakeStore struct {
	catalog  map[string]Rates
	override map[ModelKey]Price
}

func (f fakeStore) CatalogPrices(context.Context) (map[string]Rates, error) {
	return f.catalog, nil
}
func (f fakeStore) ModelPriceOverrides(context.Context) (map[ModelKey]Price, error) {
	return f.override, nil
}

func resolver(t *testing.T, s fakeStore) *Resolver {
	t.Helper()
	if s.catalog == nil {
		s.catalog = map[string]Rates{}
	}
	if s.override == nil {
		s.override = map[ModelKey]Price{}
	}
	r := NewResolver(s)
	if err := r.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return r
}

// The same model on two providers is two different things to buy. An operator
// who corrects the price of the one they get cheaply must not have that price
// applied to the one they pay list for.
func TestAnOverrideNeverLeaksToAnotherProvider(t *testing.T) {
	r := resolver(t, fakeStore{
		catalog: map[string]Rates{"claude-sonnet-4-5": {Price: Price{Input: f(3), Output: f(15)}}},
		override: map[ModelKey]Price{
			{ProviderID: 2, Model: "claude-sonnet-4-5"}: {Input: f(1), Output: f(5)},
		},
	})

	cheap, src := r.Resolve(2, "claude-sonnet-4-5")
	if src != SourceOverride || cheap.Input == nil || *cheap.Input != 1 {
		t.Errorf("provider 2 = %v from %q, want the operator's own price", cheap.Input, src)
	}
	list, src := r.Resolve(1, "claude-sonnet-4-5")
	if src != SourceCatalog || list.Input == nil || *list.Input != 3 {
		t.Errorf("provider 1 = %v from %q, want the official price", list.Input, src)
	}
}

// An override drops the vendor's long-context tier with the rest of its
// schedule. The operator stated one set of numbers; charging them a multiple
// they never mentioned would be putting a price in their mouth, and deriving
// one from their base price would be worse.
func TestAnOverriddenModelIsChargedFlat(t *testing.T) {
	r := resolver(t, fakeStore{
		catalog: map[string]Rates{"gpt-5.5": {
			Price: Price{Input: f(5), Output: f(30)},
			Tier:  &Tier{AboveTokens: 272_000, Price: Price{Input: f(10), Output: f(45)}},
		}},
		override: map[ModelKey]Price{
			{ProviderID: 1, Model: "gpt-5.5"}: {Input: f(2), Output: f(12)},
		},
	})

	rates, src := r.Resolve(1, "gpt-5.5")
	if src != SourceOverride {
		t.Fatalf("source = %q, want the operator's own price", src)
	}
	if rates.Tier != nil {
		t.Error("an overridden model kept the vendor's long-context tier")
	}
	usd, _, note := r.CostOf(1, "gpt-5.5", Tokens{Input: 500_000, Output: 1000})
	if usd == nil {
		t.Fatal("no cost")
	}
	if want := 500_000*2/1e6 + 1000*12/1e6; math.Abs(*usd-want) > 1e-9 {
		t.Errorf("cost = %v, want the operator's flat %v", *usd, want)
	}
	if note != "" {
		t.Errorf("note = %q on a flat price", note)
	}
}

// A model in no catalog and with no override has no price, and a request
// against it records an unknown cost rather than a free one.
func TestAnUnknownModelPricesToNothingAtAll(t *testing.T) {
	r := resolver(t, fakeStore{})

	if _, src := r.Resolve(1, "something-local"); src != SourceUnknown {
		t.Errorf("source = %q, want unknown", src)
	}
	usd, src, _ := r.CostOf(1, "something-local", Tokens{Input: 1000, Output: 500})
	if usd != nil {
		t.Errorf("cost = %v, want no cost at all — nobody knows what this model charges", *usd)
	}
	if src != SourceUnknown {
		t.Errorf("source = %q, want unknown", src)
	}
}

// The catalog is keyed by model, so a router that passes the vendor's own id
// through gets the vendor's price without anyone configuring anything.
func TestAPrefixedModelIdStillFindsTheOfficialPrice(t *testing.T) {
	r := resolver(t, fakeStore{
		catalog: map[string]Rates{"claude-sonnet-4-5": {Price: Price{Input: f(3), Output: f(15)}}},
	})
	got, src := r.Resolve(7, "anthropic/claude-sonnet-4-5")
	if src != SourceCatalog || got.Input == nil || *got.Input != 3 {
		t.Errorf("resolved %v from %q, want the official 3", got.Input, src)
	}
}
