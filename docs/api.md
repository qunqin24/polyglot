# Calling the Polyglot API

Use a **Polyglot API key** (`pg_…`) to call models. The provider's credential stays in Polyglot; never put it in a client request. Set `BASE` to your Polyglot origin (for example `http://localhost:3000`) and `KEY` to a key created in **API Keys**. Register a model on a provider before calling it. All paths below are relative to `BASE`.

```sh
export BASE=http://localhost:3000
export KEY=pg_your_key
```

The request format is determined by the **client endpoint**, not by the upstream provider. A request sent to `/v1/chat/completions` returns an OpenAI Chat Completions response even when its model routes to Gemini or Anthropic. The five HTTP protocols convert through the canonical model; Gemini Live uses its separate WebSocket session protocol.

## Authentication and model names

Send `Authorization: Bearer $KEY` for OpenAI-style calls, `x-api-key: $KEY` for Anthropic, or `x-goog-api-key: $KEY` for Gemini. All three identify the same kind of Polyglot key. Gemini clients may also send `?key=...`, but headers avoid exposing credentials in URLs.

Choose a model in any of these forms:

| Model value | Meaning |
|---|---|
| `gemini-2.5-flash` | A registered upstream model ID; if several providers offer it, provider priority determines the route. |
| `my-fast-model` | An optional alias configured in Polyglot. |
| `google::gemini-2.5-flash` | A registered model on the named provider. Use `::` because model IDs can contain `/`. |

Unregistered names return `404` rather than being forwarded to an upstream. An API key may also restrict which model names it permits.

## Discover available models

```sh
curl "$BASE/v1/models" -H "Authorization: Bearer $KEY"
curl "$BASE/v1/models/gemini-2.5-flash" -H "Authorization: Bearer $KEY"
curl "$BASE/v1beta/models" -H "x-goog-api-key: $KEY"
```

`GET /v1/models` returns an OpenAI-style `{"object":"list","data":[{"id":"…","object":"model"}],"has_more":false}`. `GET /v1/models/{model}` returns one entry or `404`. The Gemini listing uses `{"models":[{"name":"models/…"}]}`. Listings include only registered models and aliases visible to that key; a provider's discovery results do not automatically become callable models. URL-encode model IDs when placing them in a path.

## OpenAI Chat Completions

`POST /v1/chat/completions` accepts Chat Completions JSON. `model` and `messages` are required. Add `"stream":true` to receive SSE rather than a single JSON response.

```sh
curl "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hello"}]}'
```

The response has `choices[].message.content` and, when the upstream reports it, `usage`. For streaming:

```sh
curl -N "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","stream":true,"messages":[{"role":"user","content":"Hello"}]}'
```

This endpoint also accepts OpenAI function tools; tool calls are returned in the client protocol's shape, including streamed argument fragments.

## OpenAI Responses

`POST /v1/responses` accepts a `model` and `input` (a string or supported structured items). Set `stream:true` for Responses-style SSE events.

```sh
curl "$BASE/v1/responses" \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","instructions":"Be concise.","input":"Hello","max_output_tokens":128}'
```

The result is a Responses object with `output[]`, `status`, and optional `usage`. Polyglot does not store conversations: send the full history on subsequent calls. `store:true` and `previous_response_id` cannot provide server-side continuation here; their loss is recorded in the request's fidelity notes.

## Anthropic Messages

`POST /v1/messages` accepts Anthropic Messages JSON. Include `max_tokens` and at least one message. Set `stream:true` for Anthropic SSE.

```sh
curl "$BASE/v1/messages" \
  -H "x-api-key: $KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'
```

The reply uses Anthropic's `content[]` and `usage` fields even if the upstream speaks a different protocol.

## Gemini generateContent

Gemini places the model in the URL. Use `POST /v1beta/models/{model}:generateContent` (also available under `/v1/`) and a `contents[]` body. The streaming method is `:streamGenerateContent?alt=sse` and returns Gemini SSE frames.

```sh
curl "$BASE/v1beta/models/gemini-2.5-flash:generateContent" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":128}}'

curl -N "$BASE/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}'
```

Non-streaming responses contain `candidates[].content.parts[]` and optional `usageMetadata`. `countTokens`, `embedContent`, and `batchEmbedContents` are not implemented.

## Gemini Interactions

`POST /v1beta/interactions` (also `/v1/interactions`) uses the separate Interactions format: `model` and `input` are required. A string is valid `input`; a structured conversation can be sent as an array of steps.

```sh
curl "$BASE/v1beta/interactions" \
  -H "x-goog-api-key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-2.5-flash","input":"Hello"}'
```

Responses carry a `steps[]` timeline. Use `"stream":true` for SSE whose JSON payloads identify events with `event_type`. Polyglot sends `store:false` to the upstream and does not honor `previous_interaction_id`; send the full conversation instead.

## Gemini Live (WebSocket)

Live is **not** `generateContent` with a Live model name. Register `gemini-3.8-live` on a Gemini provider, then connect with the Google Gen AI Go SDK's `Live.Connect` to:

```text
ws://localhost:3000/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent
```

Use `wss://` behind HTTPS. Authenticate with the Polyglot key in `x-goog-api-key`. The Go SDK constructs this path from its `BaseURL`; for local HTTP development set `BaseURL` to `ws://localhost:3000` (the SDK otherwise upgrades `http://` to `wss://`). For HTTPS deployments set it to your HTTPS origin. Set `APIVersion` to `v1beta` and `Backend` to `BackendGeminiAPI`. On connect, the SDK sends `setup` with `model` and waits for `setupComplete`; subsequent messages can include `realtimeInput.audio`, `clientContent`, and `toolResponse`. Audio output, transcripts, interruptions, and tool calls return as Live server messages. A session is logged once when it ends; token cost is known only if the upstream reports usage.

See the [README's multimodal overview](../README.md) for setup notes. Other realtime client protocols are not implemented.

## Errors and limits

Error bodies follow the client endpoint's protocol. Check the **HTTP status** before parsing the protocol-specific error JSON:

| Status | Typical cause |
|---|---|
| `400` | Invalid JSON, required field missing, or an unsupported method. |
| `401` | Missing, invalid, disabled, or expired Polyglot API key. |
| `403` | This key cannot use the requested model or its budget has been exhausted. |
| `404` | Model/alias not registered or not visible to the key. |
| `429` | An API key request/token limit or a retryable budget window. Check `Retry-After` when present. |
| `502` / `503` / `504` | Upstream failure, overload, or timeout. |

After a Live WebSocket upgrade, errors close the socket rather than return another HTTP response. Unknown setup models and key policy violations close with a policy error. Request-size and upstream-size limits also apply; client disconnects cancel the upstream connection. Protocol conversion that cannot preserve a field is recorded as a **fidelity note** in request logs, not silently discarded.

## Administrator and log APIs

The operator's `/api/*` endpoints are separate from model calls. They use an **administrator session cookie**, not a `pg_…` model key. `POST /api/auth/login` accepts `{"username":"…","password":"…"}` and sets session and `polyglot_csrf` cookies. Send the session cookie on subsequent requests and echo the CSRF cookie as `X-CSRF-Token` on state-changing requests.

For example, after signing in, `POST /api/providers` accepts `{"name":"google","protocol":"gemini","base_url":"https://generativelanguage.googleapis.com","api_key":"…","models":[{"id":"gemini-2.5-flash"}]}`. `POST /api/keys` accepts `{"name":"my-client"}` and returns `{"key":{…},"secret":"pg_…"}`; handle the secret as a credential. `GET /api/providers`, `/api/models`, `/api/aliases`, `/api/keys`, and `/api/logs` expose the operator's resources. The admin session is required for each. `GET /api/setup` reports whether initial setup is needed, and first-time `POST /api/setup` additionally requires `X-Polyglot-Setup-Token`.

For read-only access to request logs using a **separate log key**, use the [content log API guide](log-api.md). A log key cannot call models or administer providers.
