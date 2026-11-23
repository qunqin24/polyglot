// Typed client for Polyglot's admin API. Every mutating call carries the CSRF
// token that the server set as a readable cookie.
import { clearQueries, invalidateQueries } from "./query-cache";

export type ProtocolName = "openai" | "openai-responses" | "anthropic" | "gemini";

export interface ProtocolInfo {
  name: ProtocolName;
  label: string;
}

export interface Provider {
  id: number;
  name: string;
  protocol: ProtocolName;
  base_url: string;
  /** Built-in multi-protocol upstream ("opencode-go", "commandcode"); empty
   *  for an ordinary provider. Its models each carry their own protocol, and
   *  `protocol`/`base_url` are display only. */
  vendor: string;
  /** The operator's own description. Nothing but the UI reads it. */
  note: string;
  has_api_key: boolean;
  headers: Record<string, string>;
  timeout_secs: number;
  enabled: boolean;
  /** Lower wins when several providers offer the same model id. */
  priority: number;
  /**
   * Stops Polyglot replaying request fields it does not recognise to this
   * upstream. Off for almost every provider: it names the exception, so a
   * provider's own parameters reach it by default.
   */
  strict_fields: boolean;
  /** Lets a rejected credential switch this provider off. Off by default: 401
   *  and 403 are not always about the key. */
  auto_disable_on_auth_error: boolean;
  /** Set when the provider switched itself off, explaining why. */
  disabled_reason: string;
  disabled_at: string | null;
  /** Set while this provider is being skipped after a recent failure.
   *  In-process state, so it is absent rather than null when healthy. */
  cooling_until?: string;
  models_synced_at: string | null;
  model_count: number;
  created_at: string;
  updated_at: string;
}

/** A real upstream model, discovered or added by hand. Callable directly. */
export interface Model {
  id: number;
  provider_id: number;
  provider_name: string;
  /** The protocol this model is actually called with: its own on a
   *  multi-protocol provider, the provider's otherwise. */
  protocol: ProtocolName;
  upstream_model_id: string;
  display_name: string;
  enabled: boolean;
  last_seen_at: string | null;
  /** What the operator typed for this model. A null field follows the
   *  catalog rather than meaning free. */
  price: Price;
  /** Another enabled provider offers the same id. */
  ambiguous?: boolean;
  /** models.dev id of the company that made it ("zhipuai" for glm-5.2,
   *  whoever serves it). Absent when the catalog does not know the id. */
  maker?: string;
  /** The maker's name for it on models.dev ("DeepSeek V4 Flash"). Absent when
   *  the catalog does not know the id; use modelLabel() to display either way. */
  catalog_name?: string;
  created_at: string;
  updated_at: string;
}

/**
 * What a model costs, in US dollars per million tokens — the unit models.dev
 * publishes. Every field is nullable because "nobody stated this" and "this is
 * zero" are different answers: a null follows the catalog, a 0 says free.
 */
export interface Price {
  input: number | null;
  output: number | null;
  cache_read: number | null;
  cache_write: number | null;
}

/**
 * What a model costs once a prompt passes a length. Several vendors charge
 * more for a long context — OpenAI above 272k tokens, Google above 200k — and
 * the whole schedule changes, not just the input price. Only the fields the
 * vendor restates are set; the rest keep the base price.
 */
export interface PriceTier extends Price {
  above_tokens: number;
}

/** A model's whole price schedule: the base price, plus the long-context tier
 *  where the vendor publishes one. An operator's own price is always flat. */
export interface Rates extends Price {
  tier?: PriceTier;
}

/** Where a price came from. Empty means nobody has one. */
export type PriceSource = "" | "models.dev" | "custom";

/** A registry row with the price in force on it. `price` is what the operator
 *  typed; `effective` is that laid over the catalog. */
export interface PricedModel extends Model {
  effective: Rates;
  source: PriceSource;
}

/** Which price snapshot is loaded. Null before one ever was. */
export interface CatalogStatus {
  version: string;
  /** "embedded" is the copy shipped in the binary; "models.dev" is a refresh
   *  the operator ran. */
  source: string;
  fetched_at: string;
  models: number;
}

/** An optional logical name pointing at a provider and model. */
export interface ModelAlias {
  id: number;
  alias: string;
  provider_id: number;
  provider_name: string;
  protocol: ProtocolName;
  upstream_model: string;
  priority: number;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** One model an upstream offers. Listing writes nothing — these are candidates
 *  until the operator ticks them. */
export interface OfferedModel {
  id: string;
  display_name?: string;
  /** Already in the registry, so the picker shows it rather than offering it. */
  registered: boolean;
  /** Set by multi-protocol vendors: the protocol this model will be called with. */
  protocol?: ProtocolName;
}

/** The outcome of listing an upstream. A failure is not an error the operator
 *  has to fix: models can always be typed in instead. */
export interface ModelOffer {
  ok: boolean;
  supported: boolean;
  error?: string;
  latency_ms: number;
  models: OfferedModel[];
}

/** A model the operator picked, on its way into the registry. */
export interface ModelChoice {
  id: string;
  display_name?: string;
}

export interface APIKey {
  id: number;
  name: string;
  prefix: string;
  enabled: boolean;
  /** Whether the secret can still be shown. False for keys created before
   *  Polyglot kept a recoverable copy — those are gone for good. */
  revealable: boolean;
  created_at: string;
  last_used_at: string | null;
  rpm: number | null;
  rph: number | null;
  rpd: number | null;
  tpm: number | null;
  tpd: number | null;
  max_concurrent: number | null;
  max_output_tokens: number | null;
  expires_at: string | null;
  allowed_models: string[];
  /** A spending cap in USD for the current window. Null is no cap. */
  budget_usd: number | null;
  /** How that cap resets: total, daily, weekly or monthly. */
  budget_period: BudgetPeriod;
  /** When the current total window began. Null means the key's creation. */
  budget_anchor: string | null;
  /** Spent in the current window. Sent only for a key that has a budget. */
  spent_usd?: number;
  /** Requests in the window whose cost nobody could work out. They are not in
   *  spent_usd, because an unknown cost is not zero. */
  unpriced_requests?: number;
  /** When the current window ends. Absent on a total, which ends when the
   *  operator resets it. Computed server-side, so it is the same instant the
   *  limiter enforces. */
  budget_resets_at?: string;
}

export type BudgetPeriod = "total" | "daily" | "weekly" | "monthly";

export interface APIKeyPolicyInput {
  /** Zero means no budget, the way zero means unlimited for the counts. */
  budget_usd: number;
  budget_period: BudgetPeriod;
  rpm: number;
  rph: number;
  rpd: number;
  tpm: number;
  tpd: number;
  max_concurrent: number;
  max_output_tokens: number;
  expires_at: string;
  allowed_models: string[];
}

export interface RequestLog {
  content_id?: string;
  content_error?: string;
  id: number;
  /** Ties this row to the server log lines, and to a trace when tracing is on. */
  request_id: string;
  started_at: string;
  finished_at: string;
  latency_ms: number;
  /**
   * Streaming measurements. Null means the value could not be measured for
   * this request — a buffered reply, or an upstream that reported no token
   * usage — never that it was zero.
   */
  ttft_ms: number | null;
  generation_ms: number | null;
  output_tps: number | null;
  status: string;
  status_code: number;
  client_protocol: string;
  upstream_protocol: string;
  provider_id: number | null;
  provider_name: string;
  model_alias: string;
  upstream_model: string;
  api_key_id: number | null;
  api_key_name: string;
  /** The address the request came from — the one a reverse proxy forwarded,
   *  unless TRUST_PROXY_HEADERS is off. */
  client_ip: string;
  /** What made the call: an app title, a referring host, or the client
   *  software. Empty when the caller identified nothing. */
  client_app: string;
  /** The end-user id and labels the client sent, if any. */
  request_user: string;
  request_metadata: string;
  stream: boolean;
  input_tokens: number;
  output_tokens: number;
  /** Parts of input_tokens, not additions to it, so a hit rate is one over the
   *  other. Rows logged before these were recorded hold 0. */
  cached_input_tokens: number;
  cache_write_tokens: number;
  reasoning_tokens: number;
  /** Upstream attempts after the first, and how many changed provider. */
  retry_count: number;
  fallback_count: number;
  error_type: string;
  error_message: string;
  fidelity_notes: string;
  /** What this request was worth at the price in force when it finished. Null
   *  is an unknown cost — no price was known, or the upstream reported no
   *  usage — and must never be shown as $0, which would claim it was free. */
  cost_usd: number | null;
  cost_source: PriceSource;
  /** What the number rests on: `long_context_price` when the prompt was long
   *  enough for the vendor's higher rate, `cache_price_assumed` when no cache
   *  price is published and the input price stood in. */
  cost_note: string;
}

/** One address an API key has been used from. */
export interface KeyOrigin {
  client_ip: string;
  requests: number;
  first_seen: string;
  last_seen: string;
}

/** How one model on one provider has actually behaved. Keyed by both, because
 *  the same model served by two providers is two different things to call. */
export interface ModelStat {
  provider_name: string;
  upstream_model: string;
  requests: number;
  errors: number;
  success_rate: number;
  last_used_at: string | null;
  /** 95th percentile, not a mean — an average hides the tail. */
  ttft_p95_ms: number | null;
  /** Median, not a mean — a very short reply produces a wild rate. */
  tps_median: number | null;
  /** How many requests the two numbers above could be measured from. */
  streamed: number;
  top_error?: string;
  /** Totals over the window. Reasoning is kept beside input/output rather than
   *  folded in: providers disagree about whether it is part of the output
   *  count, so any sum would be wrong for some of them. */
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  /** The cached share of input_tokens over the window. cache_hit_rate is a
   *  fraction of tokens rather than of requests, and is null when nothing in
   *  the window reported any input — which is not the same as a cache that
   *  missed every time. */
  cached_input_tokens: number;
  cache_write_tokens: number;
  cache_hit_rate: number | null;
}

export interface FidelityNote {
  stage: string;
  field: string;
  fidelity: "exact" | "semantic" | "lossy" | "unsupported";
  detail: string;
}

export interface RequestLogDetail extends RequestLog {
  fidelity: FidelityNote[];
}

export interface Stats {
  total_requests: number;
  success_count: number;
  error_count: number;
  input_tokens: number;
  output_tokens: number;
  /** Parts of input_tokens and output_tokens, never additions to them. */
  cached_input_tokens: number;
  cache_write_tokens: number;
  reasoning_tokens: number;
  avg_latency_ms: number;
  p95_latency_ms: number;
  /** What the priced requests in the window came to, and how many are missing
   *  from that sum because nobody knew what they cost. The second number is
   *  what keeps the first honest. */
  cost_usd: number;
  unpriced_requests: number;
  /** Went out in a protocol other than the one it arrived in, and carried at
   *  least one note saying something did not survive that. */
  converted_requests: number;
  lossy_requests: number;
  by_provider: { provider_name: string; count: number; error_count: number }[];
  /** The width of one point in `series`, chosen from the window so a chart
   *  lands between roughly 12 and 56 points whatever range was picked. */
  bucket_seconds: number;
  series: Bucket[];
}

/** One point on the Overview timeline. A bucket nobody made a request in is
 *  present with zero counts, never omitted: a line drawn straight across a gap
 *  claims traffic that did not happen. */
export interface Bucket {
  start: number;
  count: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
}

/** The conversion panel: which protocol came in, which went out, and what did
 *  not survive the trip. Loaded when the panel is opened. */
export interface ConversionStats {
  total_requests: number;
  converted_requests: number;
  lossy_requests: number;
  pairs: {
    client_protocol: string;
    upstream_protocol: string;
    count: number;
    errors: number;
  }[];
  flows: { client_protocol: string; provider_name: string; count: number }[];
  /** One row per note, so a request that lost two fields counts in two rows. */
  fields: { field: string; fidelity: FidelityNote["fidelity"]; count: number }[];
}

export interface LatencyStats {
  bucket_seconds: number;
  series: { start: number; count: number; p50: number; p95: number; p99: number }[];
  /** upper_ms is 0 on the last bar, which has no upper bound. */
  histogram: { upper_ms: number; count: number }[];
  errors: { status_code: number; error_type: string; count: number }[];
}

export interface CostStats {
  cost_usd: number;
  unpriced_requests: number;
  input_tokens: number;
  cached_input_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  bucket_seconds: number;
  starts: number[];
  /** One band per model, `points` aligned with `starts`. An empty model name
   *  is the folded remainder, so the bands still add up to the total. */
  stacks: { provider_name: string; model: string; points: number[] }[];
  models: {
    provider_name: string;
    model: string;
    cost_usd: number;
    requests: number;
    unpriced: number;
  }[];
}

export type SpendRange = "24h" | "7d" | "30d" | "month";
export type SpendDimension = "model" | "provider" | "key";

/** The Overview's spend report. Like every cost in Polyglot it is an estimate
 *  from published prices; unpriced requests are counted, never added as 0. */
export interface SpendStats {
  range: SpendRange;
  from: number;
  to: number;
  prev_from: number;
  /** Where the period would end if it ran its course (end of the month). */
  period_end: number;
  bucket_seconds: number;
  cost_usd: number;
  requests: number;
  unpriced_requests: number;
  prev_cost_usd: number;
  prev_unpriced_requests: number;
  input_tokens: number;
  cached_input_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_savings_usd: number;
  cache_savings_partial: boolean;
  dimension: SpendDimension;
  starts: number[];
  /** Up to five bands in `rows` order, then one with key "" for the rest. */
  stacks: { key: string; provider_name?: string; points: number[] }[];
  rows: {
    key: string;
    provider_name?: string;
    cost_usd: number;
    requests: number;
    unpriced: number;
    input_tokens: number;
    output_tokens: number;
  }[];
  /** Display names for model ids in rows and stacks. */
  names: Record<string, string>;
  /** Set when request logs before this moment (Unix seconds) were pruned by
   *  the retention setting and the previous period reaches back past it, so
   *  its total is incomplete and must not be compared against. */
  retained_since?: number;
}

/** One request as the live stream reports it. Times are Unix milliseconds. */
export interface LiveRequest {
  id: string;
  started_at: number;
  first_token_at: number | null;
  ended_at: number | null;
  model: string;
  provider: string;
  provider_id: number;
  api_key: string;
  client_protocol: string;
  upstream_protocol: string;
  stream: boolean;
  status: "running" | "ok" | "error";
  status_code: number;
  error_type: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number | null;
}

export type LiveEvent =
  | { type: "snapshot"; requests: LiveRequest[] }
  | { type: "start" | "end"; request: LiveRequest }
  | { type: "first_token"; id: string; at: number }
  | { type: "progress"; id: string; output_tokens: number };

export interface ContentLogSettings {
  enabled: boolean;
  retention_days: 3 | 7 | 30;
}
export interface LogKey {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
}
export interface ContentStage {
  id: string;
  kind: string;
  attempt?: number;
  protocol?: string;
  provider?: string;
  method?: string;
  url?: string;
  headers?: Record<string, string[]>;
  status?: number;
  bytes: number;
  complete: boolean;
}
export interface ContentManifest {
  version: number;
  complete: boolean;
  stages: ContentStage[];
}
export interface ContentPage {
  data: string;
  next_offset: number;
  total_bytes: number;
  has_more: boolean;
  complete: boolean;
}

export interface Me {
  username: string;
  created_at: string;
  version: string;
  data_dir: string;
  log_retention: number;
  dropped_logs: number;
  upstream_timeout: string;
  /** IANA name the WebUI formats every timestamp in. Always set; UTC by default. */
  timezone: string;
}

/** Settings edited on the WebUI's settings page; they used to be environment
 *  variables. Metrics and tracing apply on restart, everything else at once. */
export interface RuntimeSettings {
  log_retention_days: number;
  provider_cooldown_secs: number;
  upstream_timeout_secs: number;
  max_request_mb: number;
  fetch_remote_media: boolean;
  max_media_mb: number;
  trust_proxy_headers: boolean;
  block_private_upstream: boolean;
  update_check_enabled: boolean;
  update_repository: string;
  metrics_enabled: boolean;
  /** Read-only here: changed only by regenerating it. */
  metrics_token: string;
  tracing_enabled: boolean;
  otlp_endpoint: string;
  otlp_headers: Record<string, string> | null;
  trace_sample_ratio: number;
  service_name: string;
}

export interface RuntimeSettingsResponse {
  settings: RuntimeSettings;
  /** A saved metrics or tracing change is waiting for a restart. */
  restart_pending: boolean;
}

export interface UpdateStatus {
  enabled: boolean;
  supported: boolean;
  current_version: string;
  channel?: "stable" | "preview" | "development";
  latest_version?: string;
  latest_tag?: string;
  update_available: boolean;
  version_url?: string;
  checked_at?: string;
  error?: string;
}

export interface TestResult {
  ok: boolean;
  error?: string;
  latency_ms: number;
  model_count?: number;
  models?: string[];
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyglot_csrf=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (method !== "GET" && method !== "HEAD") {
    headers.set("X-CSRF-Token", csrfToken());
  }

  const res = await fetch(path, { ...init, headers, credentials: "same-origin" });
  const text = await res.text();
  let payload: unknown = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { error: text };
    }
  }

  if (!res.ok) {
    const message =
      (payload as { error?: string } | null)?.error ?? `Request failed (${res.status})`;
    const error = new ApiError(res.status, message);
    if (res.status === 401) clearQueries(error);
    throw error;
  }
  if (method !== "GET" && method !== "HEAD") {
    if (path === "/api/setup" || path === "/api/auth/login" || path === "/api/auth/logout") clearQueries();
    else if (!/\/(test|discover|secret)$/.test(path)) invalidateQueries();
  }
  return payload as T;
}

const body = (v: unknown) => JSON.stringify(v);

export const api = {
  setupStatus: () => request<{ needs_setup: boolean; version: string }>("/api/setup"),
  setup: (username: string, password: string, timezone: string, setupToken: string) =>
    request<{ username: string }>("/api/setup", {
      method: "POST",
      headers: { "X-Polyglot-Setup-Token": setupToken },
      body: body({ username, password, timezone }),
    }),
  updateSettings: (settings: { timezone: string }) =>
    request<{ timezone: string }>("/api/settings", { method: "PUT", body: body(settings) }),
  spend: (range: SpendRange, dim: SpendDimension) =>
    request<SpendStats>(`/api/stats/spend?range=${range}&dim=${dim}`),
  runtimeSettings: () => request<RuntimeSettingsResponse>("/api/settings/runtime"),
  updateRuntimeSettings: (settings: RuntimeSettings) =>
    request<RuntimeSettingsResponse>("/api/settings/runtime", { method: "PUT", body: body(settings) }),
  regenerateMetricsToken: () =>
    request<RuntimeSettingsResponse>("/api/settings/runtime/metrics-token", { method: "POST" }),
  login: (username: string, password: string) =>
    request<{ username: string }>("/api/auth/login", { method: "POST", body: body({ username, password }) }),
  logout: () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),
  me: () => request<Me>("/api/auth/me"),
  updateStatus: (refresh = false, signal?: AbortSignal) =>
    request<UpdateStatus>(`/api/update${refresh ? "?refresh=true" : ""}`, { signal }),
  changePassword: (current_password: string, new_password: string) =>
    request<{ ok: boolean }>("/api/auth/password", {
      method: "POST",
      body: body({ current_password, new_password }),
    }),

  protocols: () => request<ProtocolInfo[]>("/api/protocols"),
  stats: (hours = 24) => request<Stats>(`/api/stats?hours=${hours}`),
  conversionStats: (hours = 24) =>
    request<ConversionStats>(`/api/stats/conversions?hours=${hours}`),
  latencyStats: (hours = 24) => request<LatencyStats>(`/api/stats/latency?hours=${hours}`),
  costStats: (hours = 24) => request<CostStats>(`/api/stats/cost?hours=${hours}`),

  providers: () => request<Provider[]>("/api/providers"),
  createProvider: (p: ProviderInput) =>
    request<{ provider: Provider; models_added: number; error?: string }>("/api/providers", {
      method: "POST",
      body: body(p),
    }),
  updateProvider: (id: number, p: ProviderInput) =>
    request<Provider>(`/api/providers/${id}`, { method: "PUT", body: body(p) }),
  deleteProvider: (id: number) =>
    request<{ ok: boolean }>(`/api/providers/${id}`, { method: "DELETE" }),
  testProvider: (p: TestInput, signal?: AbortSignal) =>
    request<TestResult>("/api/providers/test", { method: "POST", body: body(p), signal }),
  providerModels: (id: number) =>
    request<{ models: string[] }>(`/api/providers/${id}/models`),
  /** Ask an upstream what it offers. Works before the provider is saved, which
   *  is the point: the picker runs inside the add dialog. */
  discoverModels: (p: TestInput, signal?: AbortSignal) =>
    request<ModelOffer>("/api/providers/discover", { method: "POST", body: body(p), signal }),
  addProviderModels: (id: number, models: ModelChoice[]) =>
    request<{ added: number }>(`/api/providers/${id}/models`, {
      method: "POST",
      body: body({ models }),
    }),

  models: (params: Record<string, string | number | undefined> = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    const qs = q.toString();
    return request<{ models: Model[]; total: number }>(`/api/models${qs ? "?" + qs : ""}`);
  },
  modelStats: (hours = 24) =>
    request<{ stats: ModelStat[]; hours: number }>(`/api/models/stats?hours=${hours}`),
  createModel: (m: ModelInput) => request<Model>("/api/models", { method: "POST", body: body(m) }),
  updateModel: (id: number, display_name: string, enabled: boolean) =>
    request<Model>(`/api/models/${id}`, { method: "PUT", body: body({ display_name, enabled }) }),
  deleteModel: (id: number) => request<{ ok: boolean }>(`/api/models/${id}`, { method: "DELETE" }),

  /** Every registered model with the price in force on it. `unpriced` counts
   *  the whole registry, not the filtered page. */
  pricing: (params: Record<string, string | number | undefined> = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    const qs = q.toString();
    return request<{
      models: PricedModel[];
      unpriced: number;
      total: number;
      catalog: CatalogStatus | null;
    }>(`/api/pricing${qs ? "?" + qs : ""}`);
  },
  /** A null field clears that price and puts the model back on the catalog. */
  setModelPrice: (id: number, price: Price) =>
    request<PricedModel>(`/api/models/${id}/pricing`, { method: "PUT", body: body(price) }),
  /** Pull current prices from models.dev. A failure is reported as ok:false —
   *  the catalog already loaded stays in place. */
  refreshCatalog: (signal?: AbortSignal) =>
    request<{ ok: boolean; error?: string; catalog?: CatalogStatus }>(
      "/api/pricing/catalog/refresh",
      { method: "POST", signal },
    ),
  aliases: () => request<ModelAlias[]>("/api/aliases"),
  createAlias: (a: AliasInput) => request<ModelAlias>("/api/aliases", { method: "POST", body: body(a) }),
  updateAlias: (id: number, a: AliasInput) =>
    request<ModelAlias>(`/api/aliases/${id}`, { method: "PUT", body: body(a) }),
  deleteAlias: (id: number) => request<{ ok: boolean }>(`/api/aliases/${id}`, { method: "DELETE" }),

  keys: () => request<APIKey[]>("/api/keys"),
  createKey: (name: string, policy: APIKeyPolicyInput) =>
    request<{ key: APIKey; secret: string }>("/api/keys", { method: "POST", body: body({ name, policy }) }),
  updateKey: (id: number, input: { name: string; enabled: boolean; policy: APIKeyPolicyInput }) =>
    request<APIKey>(`/api/keys/${id}`, { method: "PUT", body: body(input) }),
  setKeyEnabled: (id: number, enabled: boolean) =>
    request<{ ok: boolean }>(`/api/keys/${id}`, { method: "PUT", body: body({ enabled }) }),
  /** Starts a fresh total window. Only a total budget has one to start. */
  resetKeyBudget: (id: number) =>
    request<APIKey>(`/api/keys/${id}/budget/reset`, { method: "POST" }),
  /** Reads back a key the operator already owns. POST so the CSRF check
   *  covers it, even though it changes nothing. */
  revealKey: (id: number) =>
    request<{ secret: string }>(`/api/keys/${id}/secret`, { method: "POST" }),
  /** Reads back the upstream credential stored on a provider. POST for the same
   *  reason as revealKey; the list only ever says has_api_key. */
  revealProviderKey: (id: number) =>
    request<{ secret: string }>(`/api/providers/${id}/secret`, { method: "POST" }),
  deleteKey: (id: number) => request<{ ok: boolean }>(`/api/keys/${id}`, { method: "DELETE" }),
  keyOrigins: (id: number, days = 30) =>
    request<{ origins: KeyOrigin[]; days: number }>(`/api/keys/${id}/origins?days=${days}`),

  logs: (params: Record<string, string | number | undefined> = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    const qs = q.toString();
    return request<{ logs: RequestLog[]; has_more: boolean; total: number }>(
      `/api/logs${qs ? "?" + qs : ""}`,
    );
  },
  contentLogSettings: () => request<ContentLogSettings>("/api/content-logging"),
  saveContentLogSettings: (input: ContentLogSettings) => request<ContentLogSettings>("/api/content-logging", { method: "PUT", body: body(input) }),
  logKeys: () => request<LogKey[]>("/api/log-keys"),
  createLogKey: (name: string) => request<{ key: LogKey; secret: string }>("/api/log-keys", { method: "POST", body: body({ name }) }),
  deleteLogKey: (id: number) => request<{ ok: boolean }>(`/api/log-keys/${id}`, { method: "DELETE" }),
  logManifest: (id: number) => request<ContentManifest>(`/api/logs/${id}/content`),
  logContent: (id: number, stage: string, offset = 0) => request<ContentPage>(`/api/logs/${id}/content/${encodeURIComponent(stage)}?offset=${offset}&limit=262144`),
  log: (id: number) => request<RequestLogDetail>(`/api/logs/${id}`),

};

export interface ProviderInput {
  name: string;
  protocol: ProtocolName;
  base_url: string;
  /** Send the provider's `vendor` back on every update: an update writes every
   *  field, so leaving it out turns a vendor provider into an ordinary one. */
  vendor?: string;
  note: string;
  api_key: string | null;
  headers: Record<string, string>;
  timeout_secs: number;
  enabled: boolean;
  priority: number;
  strict_fields: boolean;
  auto_disable_on_auth_error: boolean;
  /** Only these are registered. Omit or leave empty to save a provider with no
   *  models — a valid configuration, not a half-finished one. */
  models?: ModelChoice[];
}

export interface TestInput {
  id: number;
  protocol: ProtocolName;
  base_url: string;
  vendor?: string;
  api_key: string | null;
  headers: Record<string, string>;
  timeout_secs: number;
}

export interface ModelInput {
  provider_id: number;
  upstream_model_id: string;
  display_name: string;
  enabled: boolean;
}

export interface AliasInput {
  alias: string;
  provider_id: number;
  upstream_model: string;
  priority: number;
  enabled: boolean;
}
