# Polyglot Development Guide

Rules for working on this repository. Read before changing anything.

## Project Purpose

**Positioning — an intelligent personal AI gateway.** One person runs it, for
their own traffic. *Intelligent* describes the plumbing, not a feature list: it
routes a model name to the right provider, converts the protocol without losing
fields, and tells the operator what a request cost. It never means adding an AI
feature on top of the gateway.

**Long-term vision — the universal compatibility layer between LLM API
protocols.** Any supported client protocol reaches any supported upstream
protocol. A fifth protocol costs one codec and every pairing with it comes free;
that property is the vision, and it is what the canonical model protects.

**Brand claim — your AI should not be defined by any one vendor.** The reason
to run this is that the client you like and the model you want are chosen by
different companies. Polyglot is a **lightweight LLM API protocol conversion
gateway** that makes that somebody else's problem.

It is **not** an API resale platform. Judge every proposed change by one
question: *is this genuinely required for protocol conversion?* If not, do not
add it.

Competitiveness comes from conversion accuracy, stable streaming, good tool-call
compatibility, simple deployment, a usable WebUI and low resource use — never
from feature count.

## Core Architecture

```
             decode                          encode
OpenAI ─────────────┐                    ┌────────▶ OpenAI
OpenAI Responses ───┤                    ├────────▶ OpenAI Responses
Anthropic ──────────┼──▶  Canonical  ──▶ ┼────────▶ Anthropic
Gemini ─────────────┘                    └────────▶ Gemini
```

Every request follows one path, including OpenAI → OpenAI:

```
client protocol → canonical → routing → upstream protocol → upstream
upstream → canonical → client protocol
```

Three concepts are **strictly separate**. Never collapse them:

- **Protocol** — a wire format: `openai`, `openai-responses`, `anthropic`,
  `gemini`, `gemini-interactions`. OpenAI ships two genuinely different wire
  formats and so does Google, so each pair is two protocols, not one protocol
  with a mode flag.
- **Provider** — a service you call: OpenRouter, DeepSeek, Google
- **Model** — a real upstream model belonging to a provider

An **alias** is an optional fourth layer: a logical name pointing at a
provider + model. Never make one mandatory.

OpenRouter, DeepSeek, SiliconFlow, Groq, vLLM and Ollama are all *providers*
speaking the *OpenAI protocol*. They share one codec. There must never be a
`protocol/openrouter.go`.

## Technology Stack

Fixed. Do not swap any of it.

| Layer | Choice |
|---|---|
| Backend | Go, `net/http`, `chi` — standard library first |
| Frontend | React 19, Vite, TypeScript, Tailwind v4, hand-written shadcn/ui components |
| Database | SQLite via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`) |
| Packaging | WebUI embedded with `go:embed`; one binary, one container, one process |
| Package manager | pnpm (see `packageManager` in `web/package.json`) |

Go 1.25+ (`go.mod` requires it) and Node 20+. pnpm is pinned by
`packageManager` in `web/package.json`; let corepack honour it.

## Repository Structure

```
cmd/polyglot/          entrypoint: config, store, HTTP server, shutdown
internal/
  canonical/           protocol-neutral Request/Response/Event, errors, fidelity
  protocol/            Codec interface + registry
    openai/            OpenAI Chat Completions codec
    responses/         OpenAI Responses codec
    anthropic/         Anthropic Messages codec
    gemini/            Gemini generateContent codec
    interactions/      Gemini Interactions codec
  provider/            how to CALL an upstream: URLs, auth headers, HTTP transport
  router/              model name → provider + upstream model
  gateway/             the request pipeline
  stream/              SSE framing, both directions
  store/               all SQLite access
  auth/                API keys and the admin session
  pricing/             official prices, and what a request cost
  usage/               buffered request logging
  telemetry/           request lifecycle, metrics, tracing
  api/                 HTTP surface: protocol endpoints, admin API, WebUI serving
  config/ idgen/ version/
migrations/            *.sql applied in filename order, embedded
tests/compatibility/   official-SDK tests — a SEPARATE Go module
web/src/
  lib/                 api client, hooks, i18n catalogs, utils
  components/ui/       shadcn-style primitives
  pages/               one file per page
```

Do not create deep directory trees. A package earns its place by having a job.

## Protocol Conversion Rules

**Never write A → B conversion.** No `openaiToAnthropic`, no
`gemini_to_openai.go`. Each protocol implements only `Protocol ↔ Canonical`, so
N protocols cost N codecs rather than N². Four protocols today; adding a fifth
is one package, and every pairing with it comes for free.

Every codec implements `protocol.Codec` (see `internal/protocol/protocol.go`):
`DecodeRequest` / `EncodeRequest` / `DecodeResponse` / `EncodeResponse` /
`DecodeStream` / `NewStreamEncoder` / `EncodeError`.

Adding a protocol means: one package, `protocol.Register` in `init()`, a blank
import in `internal/api/server.go`, endpoints in the router. Nothing else.

**Never silently drop a field.** Protocols are not equivalent. Record what
happened via `canonical.Diagnostics`:

- `FidelityExact` — direct, identical counterpart
- `FidelitySemantic` — expressed differently, same meaning
- `FidelityLossy` — carried over with a loss
- `FidelityUnsupported` — the target cannot express it

Notes surface in request logs and in the Protocol Inspector. A dropped field
with no note is a bug.

`internal/protocol/matrix_test.go` walks all 5×5 protocol pairs and enforces the
rule above: a field must either survive the round trip **or** produce a fidelity
note. Any codec change must keep it green.

**Unrecognised fields are carried, not dropped.** A codec decodes into a struct
and `encoding/json` throws away everything the struct does not name — which is
a silent drop, and therefore a bug by the rule above. Each codec calls
`protocol.Capture` on decode and `protocol.Merge` on encode
(`internal/protocol/passthrough.go`), so a provider's own parameters —
OpenRouter's `provider`, vLLM's `guided_json`, DeepSeek's `prefix` — survive.

**A struct member nothing reads is worse than no struct member.** Naming a
field excludes it from `Capture`, so a field that is declared but never used is
deleted with no extension to replay it and no note to record it — the exact
silent drop the rule above forbids, hidden behind an honest-looking type. Two
choices only: read it into canonical, or leave it out of the struct so
passthrough carries it.

**Passthrough only reaches the top level, so a nested vendor field must be
modelled.** `Capture` walks the fields named in `Top`/`Nested`; anything inside
a known member is re-encoded from canonical, and what canonical does not hold
is gone — no extension, no note. That is how Anthropic's `cache_control`
disappeared: three levels down inside `messages[].content[]`, silently turning
off the prompt caching the caller was paying for while the request still
succeeded. `canonical.CacheHint` exists for exactly that reason.
`TestSameProtocolRoundTrip` is the guard: it decodes and re-encodes a rich
request in the *same* protocol and requires every difference to be a listed,
justified normalisation. Same protocol in and out means no mismatch can excuse
a loss, so it is the sharpest test of the canonical model there is.

**A field with an exact counterpart elsewhere belongs in canonical, not in
passthrough.** Extensions are replayed only on a same-protocol route and
reported as unsupported on the other four, so leaving a convertible field to
`Capture` turns a clean conversion into a false loss report — Gemini's
`createTime` is OpenAI's `created`, and used to be announced as a dropped
Gemini-specific field.

**Where protocols count the same thing differently, canonical picks one
meaning and says so.** `canonical.Usage.InputTokens` is the whole prompt,
cached portion included; `CachedInputTokens` and `CacheWriteTokens` are parts
of it. OpenAI, Responses and Gemini already count that way. Anthropic does not
— its `input_tokens` excludes both cache portions — so its codec sums on decode
and splits on encode. Left undefined, one field arrives meaning two things and
an OpenAI client is told its cached tokens outnumbered its prompt.
`TestAnthropicCacheCountsAreConvertedNotCopied` pins it.

**Provider-executed tools are carried the same way.** Gemini's `googleSearch`,
Anthropic's `web_search`, OpenAI's `file_search` run inside the provider, so
there is nothing to relay and no honest cross-protocol mapping. They go into
`canonical.Request.NativeTools` and come back through
`protocol.MergeNativeTools`. Never invent a translation between them. Read them
from the raw request rather than a fixed list of names, so a tool a vendor
ships next year survives instead of vanishing.

The rule lives in `Merge` so no codec can get it wrong: extensions are replayed
**only when the source and target protocol are the same**, and are reported as
`FidelityUnsupported` otherwise. Never forward one across dialects; a field
Polyglot could not translate becomes an upstream 400. An extension never
overwrites a member the encoder produced — the routed model name wins over
anything in the body. This is not a passthrough mode and must not grow into
one: there is still exactly one code path through canonical.

**A replay token is not content, and must not be filtered like content.** A
stream event carrying only a signature has no text, and a `if ev.Text == ""`
guard silently threw it away — so the client never received the token, its own
history came back unsigned, and Polyglot then reported the reasoning as a
conversion loss it had caused itself a turn earlier. Check the token before the
emptiness check, the way `interactions/stream.go` does.

**A replay token can arrive on a part that is not the one it describes.**
Gemini closes a thinking block with a `thoughtSignature` on the part that
*follows* it — for a plain answer that is the text part, which had no home in
canonical because reasoning and tool calls each had their own.
`ContentPart.Signature` and `Event.Signature` are that home. When adding a
protocol, check every part type the token can land on, not just the obvious one.

**Gemini signs a thinking block, not a part.** The thought arrives as a run of
thought parts with the signature closing the run on a part of its own. Judging
each part separately discards the text of a block that is perfectly replayable
and reports one loss per fragment; `signedThoughtRuns` makes the run the unit.
`TestReplayingAStreamedThoughtLosesNothing` walks the whole loop — stream out,
client replays, request back in — because neither half of that bug is visible
from one direction alone.

**Provider-bound replay tokens must survive a round trip through the client.**
Gemini 3 signs each function call and rejects the next request when the first
call in a step comes back unsigned, so `canonical.ToolCall.Signature` travels out
to the client and back in the `extra_content.google.thought_signature` envelope
Google defined (`internal/protocol/extension.go`) — reuse that spelling, do not
invent one. It is never sent to an upstream that did not define it; note it as
lossy instead. History that has no signature at all gets Google's documented
placeholder plus a note, because an unsigned call is a hard 400.

## Provider and Model Rules

**Discovery proposes; the operator disposes.** Listing an upstream shows what is
on offer and writes nothing. Only models the operator ticked in the picker are
registered. Nothing may ever enter the registry that nobody chose — that is the
rule this section exists to protect.

- **Discovery is a capability, not a requirement.** A driver opts in by
  implementing `provider.ModelDiscoverer`. Listing runs only when asked for:
  `POST /api/providers/discover`, from the picker in the provider dialog. It
  works before the provider exists, using the credentials in the form.
- **Listing failure must never fail the save.** Report it as information
  (`ok:false` plus `supported`), never as a creation error. A provider whose
  upstream cannot list models is normal; models get typed in instead.
- **A provider with no models is a valid configuration**, not a half-finished
  one. Models get added to it later.
- **Manual models are first-class.** A hand-typed model is callable exactly like
  one picked from a listing.
- **Membership belongs to the provider.** Which models a provider exposes is
  added and removed on that provider. The Models page answers a different
  question — what does this gateway serve — and carries no add or delete.
- **Aliases are optional.** They exist to give a short, stable name that an
  operator can repoint. Never make one mandatory.

Resolution order in `internal/router` — most specific first:

1. `provider::model` — the provider was named outright (`::`, not `/`, because
   model ids routinely contain slashes)
2. an alias
3. a real upstream model id from the registry

A name matching none of them is an error. Nothing is forwarded on the chance an
upstream might recognise it: a typo must come back as Polyglot saying it does
not know the model, not as a vendor's 404.

**Ambiguity must be deterministic.** When several providers offer the same model
id, order by provider `priority` (highest first) then provider `id` — a total,
stable order. Within one priority level `router.PreferProtocol` moves a
provider that speaks the client's protocol ahead of one that would need
converting, because only a same-protocol route carries extensions and native
tools. It must never cross a priority boundary and must never drop a
candidate: priority is the operator's stated intent, and every protocol
converts to every other one, so a mismatch is a preference and not a
disqualification.
Never pick at random, and do not build a load balancer to solve it.

**Re-reading a listing never deletes.** A model missing from one listing keeps
its row and its older `last_seen_at`. Never overwrite an operator's `enabled`
flag or a display name they set. See
`TestSyncDoesNotDeleteOrOverrideOperatorChoices`.

## Pricing Rules

Polyglot prices the requests it logs so an operator can see where their spend
goes. That is **cost visibility, not billing**, and the distinction is the
reason this section exists: no balance, no top-up, no deduction from anything,
no invoice. The Non-Goals list still stands.

**One exception, and only one: a per-key budget may refuse a request.** A key
handed to someone else needs a ceiling on what it can cost, and "watch the
dashboard" is not one. `api_keys.budget_usd` is a cap in dollars over a window
the operator picks — a total they reset by hand, or daily, weekly or monthly in
UTC. Null means no cap, which is what every key had before it existed, and what
almost every key still has. It is enforced beside the RPM and TPD limits in
`internal/auth/limits.go`, not anywhere new.

**A budget is approximate, and the UI says so rather than pretending
otherwise.** A request is priced after it finishes, so the one that crosses the
line has already been paid for; and a model with no price adds nothing to the
total, because rule 21 holds here too — an unknown cost is not zero. A budget
therefore caps roughly, and never at all on traffic nobody can price. Say that
in the interface; do not "fix" it by treating a missing price as free.

**Nothing else about money may refuse anything.** Not a provider's spend, not a
global total, not a monthly bill. One cap, on one key, checked in one place.

Resolution order, most specific first:

1. the operator's own price on that model
2. the official vendor price from the models.dev catalog
3. nothing — the cost is **unknown**

**Unknown is never zero.** A zero says the request was free, which is a claim
nobody made. `request_logs.cost_usd` is null, the UI shows a dash, and any
total reports how many rows are missing from it. A stated 0 is a different
thing and is kept: an operator may say a model is free.

**Only first-party prices are ingested.** models.dev catalogues ~190
providers, most of them resellers quoting their own margin —
`claude-sonnet-4-5` appears under ten at three prices. `firstParty` in
`internal/pricing/catalog.go` is the hand-kept list of vendors, ordered so a
tie is total. Never take a reseller's number for another reseller's model;
a cheaper route is what an override is for.

**An override is four nullable numbers, never a copy of the catalog row.** A
blank field follows the catalog, so correcting one price still tracks an
official cut in the others. Clearing all four returns the model to the catalog
as it stands today.

**A catalog refresh never touches an operator's price** — the same rule model
discovery follows for the registry. The catalog lives in its own table;
overrides live on the model row.

**The cost is snapshotted onto the log row when the request finishes**, so
changing a price does not rewrite history, and a price added later does not
backfill rows logged without one.

**Cache portions are parts of the prompt, not additions to it.** The formula is
`UncachedInputTokens x input + cached x cache_read + written x cache_write +
output x output`. A missing cache price falls back to the input price and
records `cache_price_assumed`. Reasoning tokens are never added — vendors
disagree about whether they are already inside the output count.

**Long-context tiers come from the catalog and never from an override.**
Several vendors charge more above a prompt length — `Rates.Tier` carries that
rung, and the threshold is compared against the whole prompt, cached portion
included. A tier keyed on anything but context size is ignored rather than
guessed at. An overridden model is charged flat: the operator stated one set of
numbers, and applying a multiple they never mentioned would put a price in
their mouth. A request charged at the higher rate records
`long_context_price`.

**Pricing never runs in the request path.** The resolver holds an in-memory
snapshot and is read on the usage logger's flush goroutine, reloaded when a
price or the catalog changes. A pricing failure costs a number, never a
request.

**The catalog snapshot is embedded and refreshed only on demand.** `make
catalog` regenerates `internal/pricing/snapshot.json`; the runtime fetch is a
GET of a public file that happens when the operator presses refresh, and sends
nothing. A failed refresh is information, not an error — the loaded catalog
stays. This is not a phone home and must never become one.

## Streaming Rules

Streaming is core, not a later patch. Never pass one vendor's SSE straight
through the system.

All streams convert through `canonical.Event`: `message.start`, `text.delta`,
`reasoning.delta`, `tool_call.start`, `tool_call.arguments.delta`,
`tool_call.end`, `usage`, `message.end`, `error`.

**Tool-call arguments arrive in fragments. Never parse a fragment.** Accumulate
raw bytes and only treat them as JSON once the call completes
(`canonical.Accumulator` does this). Gemini needs a whole `functionCall` object,
so its stream encoder buffers — that is a property of the target protocol, not a
shortcut to copy elsewhere.

Also required:

- Propagate `context.Context`; a disconnected client must tear down the upstream
- Always close `resp.Body`
- Reuse the shared `http.Transport`; never build one per request
- Flush after every SSE frame
- Distinguish client disconnect from upstream failure in logs (status
  `cancelled`, not `error`)

**Gemini Interactions has no Go SDK.** `google.golang.org/genai` ships no
Interactions client in any released version, so that protocol cannot have the
official-SDK compatibility test the other four get. Do not paper over that with
a direct codec call dressed up as one — rule 19 exists for this. Its wire types
came from the schema the official TypeScript provider validates against plus
recorded traffic; where Google's prose docs disagreed with the recording, the
recording won. When a Go SDK ships Interactions support, add the missing layer.

## Telemetry Rules

**No phone home. Ever.** Polyglot has no telemetry server and must never grow
one. Do not add usage analytics, anonymous statistics, install or version
counters, crash upload, or any code path that sends anything to this project's
authors. The only egress is an OTLP endpoint the operator configured, to a
collector the operator runs.

**Telemetry is never more important than the request.** Every recording path is
non-blocking and best effort. A dead collector, a saturated registry or a panic
inside `internal/telemetry` must cost a counter, never a request. Nothing in the
request path waits on an exporter.

**Privacy is structural, not a filter.** Prompts, completions, tool arguments,
headers, credentials, query strings, request bodies and upstream error text are
never passed into `internal/telemetry`, so there is nothing there to leak. Add a
new attribute only if it is protocol, provider, model, stream, status, an error
class or a count.

**Metric labels come from a bounded set.** Provider and model are labels because
an operator configured them. A model name a client invented is `unrouted`.
Request ids, trace ids, API keys, IPs, URLs and error messages are never labels
— they belong on a span or a log row, which cost one record rather than one
time series. The registry caps distinct series per metric and folds the rest
into an overflow series; do not remove that cap.

**One lifecycle object, not scattered counters.** `telemetry.Request` measures a
request once, and both the Prometheus metrics and the request log row are filled
from it. Business code calls `StartRequest`, `StartAttempt`, `ContentToken`,
`Usage`, `Finish` — never a Prometheus counter or a span exporter directly.

**Nothing per token.** No span per delta, no log line per chunk, no buffering of
a streamed response to count anything. `ContentToken` is one comparison and one
clock read, and that is the ceiling for the streaming hot path.

**Say what is not implemented.** OTLP over gRPC and metrics-over-OTLP do not
exist here. Do not document an exporter that has not been written.

## Database Rules

- **Every schema change needs a migration** in `migrations/`, named
  `NNNN_description.sql`. They run in filename order at startup.
- **Never edit an applied migration.** Add a new one.
- **Old databases must upgrade cleanly.** Test against a database created by the
  previous version before claiming a migration works.
- All SQL lives in `internal/store`. Business code calls typed methods.
- **Never return a nil slice in a JSON response** — it marshals to `null` and
  breaks typed clients. Initialise to `[]`. See `internal/api/nonnull_test.go`.
- Never write to SQLite per streamed chunk. One log row per completed request,
  buffered through `internal/usage`.

## Frontend Rules

- `strict: true` is on and stays on, along with `noUnusedLocals` and
  `noUnusedParameters`.
- ESLint 9 flat config (`web/eslint.config.js`) with type-aware
  `typescript-eslint`, React, React Hooks and React Refresh. One linter only —
  do not add Biome or a second formatter.
- **No `any`, `@ts-ignore`, `@ts-expect-error`, or `eslint-disable` to silence a
  problem.** Fix the cause. For untrusted external data use `unknown` and narrow
  it (`parseHeaders` in `pages/providers.tsx` is the pattern).
- i18n: `web/src/lib/i18n/en.ts` is the source of truth; its keys define
  `TranslationKey`, so a missing key in `zh.ts` is a compile error. **Every
  user-visible string goes through `t()`.** Placeholders use `{name}`.
- No heavy state library. `useAsync` in `lib/hooks.ts` is the data layer.
- Keep the UI restrained: it should look like infrastructure, not a dense admin
  console. One well-chosen number beats a chart.

## Security Rules

- Provider credentials are encrypted at rest (AES-256-GCM, key in
  `$DATA_DIR/secret.key`). `store.Provider.APIKey` is `json:"-"` and must never
  reach the browser — not even to the form that edits it.
- Polyglot's own API keys are stored as SHA-256 hashes and shown exactly once.
- **Strip upstream credentials from every error path** before it reaches a
  client or a log (`redact` / `redactSecret`). There is a test for this.
- Request logs record metadata only. **Never store prompts or completions.**
- Never log headers.
- Admin session: HttpOnly cookie plus a double-submit CSRF token on every
  state-changing request.
- Keep the input size limits, upstream response caps and timeouts.
- Validate provider base URLs (scheme, no embedded credentials). Never follow a
  cross-host redirect with the auth header attached.

## Scope / Non-Goals

**Do not add infrastructure:** Redis, PostgreSQL, MySQL, Kafka, RabbitMQ,
separate workers, a separate frontend server, Nginx, microservices, or a
scheduler. Production is one container, one process, one SQLite file.

**Do not implement:** payments, top-ups, redemption codes, referrals, user
plans, billing, a shop, tickets, announcements, OAuth/SSO, multi-tenancy,
organisations, RBAC, image/video/music generation, realtime voice, WebRTC, vector
databases, RAG, agents, or MCP.

The per-key spending budget in the Pricing Rules is the one thing on this list
that got carved out, and it stays carved narrowly: a cap on one key, enforced
next to that key's other limits. It is not a balance to be topped up, and
nothing else may grow out of it.

**Planned but not implemented — say so, never fake it:** audio input,
embeddings, and token counting. These are deferred, not rejected; they do not
belong in the Non-Goals list above.

Audio specifically: it must stay *reported*, and must never be forwarded in
another shape. `audio/*` and `video/*` are refused by
`protocol.ClassifyMedia`, because without that they fall through to "file" and
reach an upstream as a document it rejects — which turns "not implemented" into
a request that fails for an unrelated-looking reason.
`TestAudioIsReportedNotSmuggledThroughAsADocument` pins this; when audio is
implemented, rewrite that test rather than deleting it.

**Multimodal:** images and PDFs convert between all four protocols. Inline
base64 is the shape everything can express and must never be lossy. A remote
URL is forwarded only to a protocol that fetches one itself; Gemini does not,
so that pairing is reported unless `FETCH_REMOTE_MEDIA` is on. A `file_id` is
provider-bound and follows the replay-token rule. `internal/media` is the only
place Polyglot dials an address a *client* chose — private ranges are refused
there unconditionally, not behind `BLOCK_PRIVATE_UPSTREAM`, and that must not
be relaxed.

## Development Workflow

- **Understand the current implementation before changing it.** Do not build a
  parallel system next to an existing one.
- **Small steps.** The repository must stay compilable, runnable and testable
  after every change.
- **Prefer incremental change** over rewriting. Never re-init the repo, swap
  frameworks, rewrite the canonical model, or redo all codecs to satisfy one
  feature request.
- **Do not abstract for a future that has not arrived.** Add the interface when
  the second implementation exists, not before.
- Never fake progress with stub endpoints or a wall of TODOs. If something
  cannot be finished, say which part and why.
- Go: idiomatic, errors wrapped with context, no interface for a single
  implementation, no boilerplate for its own sake.

Local development, two terminals:

```bash
make web-dev    # Vite on :5173
make dev        # Go API on :3000, proxying the UI to Vite
```

## Testing Rules

Three layers, kept separate. None replaces another.

| Layer | Where | What it proves |
|---|---|---|
| Codec tests | `internal/protocol/*/` | protocol JSON ↔ canonical, plus the 4×4 matrix |
| Integration / wire tests | `internal/api/` | HTTP, SSE, router, provider, gateway |
| Official-SDK compatibility | `tests/compatibility/` | a real vendor SDK can use Polyglot |

### The runtime never uses a vendor SDK; the tests always do

**Polyglot implements every protocol itself** — `net/http`, `encoding/json`, its
own SSE parser and writer. Never import the OpenAI, Anthropic or Google SDK into
runtime code to perform conversion or to call an upstream. The whole value of
this project is owning the wire format.

**But compatibility must be proven with the real SDKs.** A codec unit test
cannot see a wrong header, a mis-parsed URL, a malformed SSE frame, a missing
terminator or an error body the SDK refuses to type. The official clients are
Polyglot's protocol-compatibility probe, not just a test tool.

`tests/compatibility/` is therefore **its own Go module**. The SDKs are test-only
dependencies: they are absent from the root `go.mod`, never linked into the
binary, and `go build ./...` at the root never resolves them.

Compatibility tests must go over a real HTTP path:

```
Official SDK -> HTTP -> Polyglot (the real built binary) -> HTTP -> mock upstream
```

The harness builds `cmd/polyglot`, runs it as a process, walks the actual
first-run flow over the admin API, and points the SDKs at it. **Never call a
codec function directly and label it an SDK compatibility test** — that proves
nothing about serialisation, headers, status codes, SSE framing or stream
termination, which is the entire point. The upstream is a local mock, so the
suite never needs a paid API key.

Cover only what Polyglot actually claims to support. Do not implement a feature
just to have something to test.

SDK versions are pinned in `tests/compatibility/go.mod`. When upgrading one:
run the full suite, and if it breaks, work out whether the vendor's protocol or
the SDK's behaviour changed and fix Polyglot. **Never pin back to an old version
to make the suite green** — a new SDK that cannot call Polyglot is a real
compatibility signal.

## Required Checks

Run this before claiming any change is done:

```bash
make check
```

It runs, in order:

```bash
go test ./...                    # Go tests: codecs, matrix, integration/wire
cd tests/compatibility && go test ./...   # official OpenAI/Anthropic/Google SDKs
pnpm --dir web run typecheck     # tsc --noEmit, strict mode
pnpm --dir web run lint          # eslint .
gofmt -l .                       # fails if anything is unformatted
go vet ./...                     # and again inside tests/compatibility
pnpm --dir web run build         # production bundle
```

The compatibility suite alone is `make compatibility-test`.

All of it must pass. Additionally:

- `go test -race ./...` after touching streaming, the gateway or the usage logger
- `make build` to confirm the WebUI still embeds into the binary
- After a migration: start the binary against a database from the previous
  version and confirm it upgrades and the old data still routes

## Things You Must Not Do

1. Write direct A → B protocol conversion instead of going through Canonical.
2. Create a codec per vendor rather than per protocol.
3. Drop a field during conversion without a `Diagnostics` note.
4. Parse a partial tool-call argument fragment as JSON.
5. Make a model mapping or alias a required setup step.
6. Delete a registered model, or overwrite an operator's `enabled` flag or
   display name, when re-reading an upstream listing.
7. Put a model in the registry that the operator did not pick.
8. Pick a provider at random when a model id is ambiguous.
9. Return a nil slice as `null` in a JSON response.
10. Edit an already-applied migration, or break upgrades from an older database.
11. Send a provider credential to the browser, or leave one in an error message
    or a log.
12. Store prompt or completion text.
13. Silence a type or lint error with `any`, `@ts-ignore`, or `eslint-disable`.
14. Add Redis, PostgreSQL, a worker, a scheduler, or a second linter.
15. Add any feature from the Non-Goals list.
16. Send telemetry, usage data or a crash report to this project's authors.
17. Put a request id, trace id, credential, IP, URL or error message in a
    Prometheus label, or let telemetry block, slow or fail a request.
18. Import a vendor SDK into runtime code, or let one into the root `go.mod`.
19. Pass off a direct codec call as an official-SDK compatibility test.
20. Downgrade a pinned SDK to make the compatibility suite pass.
21. Record an unknown cost as zero, price a model from a reseller's catalog
    entry, or let a catalog refresh overwrite a price the operator typed.
22. Report work as complete without running `make check`.
