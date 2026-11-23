import type { ProtocolName } from "@/lib/api";
import type { BrandName } from "@/components/brand-icon";

// Presets cover the providers people actually add first. Each one is a Base
// URL plus a protocol — never a bespoke integration.
export type ProviderPreset = {
  label: string;
  protocol: ProtocolName;
  base_url: string;
  /** The vendor's mark, so the list is scanned rather than read. Two presets
      may share one — the Responses API is still OpenAI. */
  brand: BrandName;
  /** Which picker tab it sits under: the vendor's own API, or a platform
      that resells or hosts other vendors' models. */
  group: "vendor" | "platform";
  agentPlatformStandard?: boolean;
  /** A built-in multi-protocol upstream. The server owns its addresses, auth
   *  and per-model protocols; `protocol` and `base_url` here are display only. */
  vendor?: string;
};

export const PRESETS: ProviderPreset[] = [
  { label: "OpenAI", protocol: "openai", base_url: "https://api.openai.com", brand: "openai", group: "vendor" },
  { label: "OpenAI (Responses API)", protocol: "openai-responses", base_url: "https://api.openai.com", brand: "openai", group: "vendor" },
  { label: "OpenRouter", protocol: "openai", base_url: "https://openrouter.ai", brand: "openrouter", group: "platform" },
  { label: "DeepSeek", protocol: "openai", base_url: "https://api.deepseek.com", brand: "deepseek", group: "vendor" },
  { label: "SiliconFlow", protocol: "openai", base_url: "https://api.siliconflow.cn", brand: "siliconcloud", group: "platform" },
  { label: "Groq", protocol: "openai", base_url: "https://api.groq.com", brand: "groq", group: "platform" },
  { label: "Ollama (local)", protocol: "openai", base_url: "http://127.0.0.1:11434", brand: "ollama", group: "platform" },
  { label: "OpenCode Go", protocol: "openai", base_url: "https://opencode.ai/inference/go", brand: "opencode", group: "platform", vendor: "opencode-go" },
  { label: "Command Code", protocol: "openai", base_url: "https://api.commandcode.ai/provider", brand: "commandcode", group: "platform", vendor: "commandcode" },
  { label: "Anthropic", protocol: "anthropic", base_url: "https://api.anthropic.com", brand: "anthropic", group: "vendor" },
  { label: "Google Gemini", protocol: "gemini", base_url: "https://generativelanguage.googleapis.com", brand: "gemini", group: "vendor" },
  {
    label: "Gemini Enterprise Agent Platform (Express)",
    protocol: "gemini",
    base_url: "https://aiplatform.googleapis.com/v1/publishers/google",
    brand: "vertexai",
    group: "platform",
  },
  {
    label: "Gemini Enterprise Agent Platform (Standard)",
    protocol: "gemini",
    base_url: agentPlatformBaseURL("PROJECT_ID", ""),
    brand: "vertexai",
    agentPlatformStandard: true,
    group: "platform",
  },
];

export function agentPlatformBaseURL(projectID: string, location: string): string {
  const region = location.trim().toLowerCase() || "global";
  const host =
    region === "global"
      ? "aiplatform.googleapis.com"
      : region === "us" || region === "eu"
        ? `aiplatform.${region}.rep.googleapis.com`
        : `${region}-aiplatform.googleapis.com`;
  const project = encodeURIComponent(projectID.trim() || "PROJECT_ID");
  return `https://${host}/v1/projects/${project}/locations/${region}/publishers/google`;
}

export function isAgentPlatformBaseURL(baseURL: string): boolean {
  try {
    const url = new URL(baseURL);
    const agentPlatformHost =
      url.hostname === "aiplatform.googleapis.com" ||
      url.hostname.endsWith("-aiplatform.googleapis.com") ||
      /^aiplatform\.(?:us|eu)\.rep\.googleapis\.com$/.test(url.hostname);
    const path = url.pathname.replace(/\/+$/, "");
    return agentPlatformHost && path.endsWith("/publishers/google");
  } catch {
    return false;
  }
}

export const emptyForm = {
  name: "",
  protocol: "openai" as ProtocolName,
  base_url: "",
  vendor: "",
  note: "",
  api_key: "",
  headers: "",
  timeout_secs: 0,
  priority: 0,
  enabled: true,
  strict_fields: false,
  auto_disable_on_auth_error: false,
};
export type ProviderForm = typeof emptyForm;

export type HeaderParse =
  | { ok: true; headers: Record<string, string> }
  | { ok: false; reason: "invalid" | "notObject" };

// JSON.parse is typed `any`, so the result is taken as unknown and narrowed
// here rather than trusted. Non-string values are stringified: a header can
// only ever be a string on the wire.
//
// Pure on purpose — it runs during render to feed the models block, and a
// function that reported failure by setting state could not.
export function parseHeaders(text: string): HeaderParse {
  if (!text.trim()) return { ok: true, headers: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, reason: "invalid" };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, reason: "notObject" };
  }
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(parsed)) {
    out[key] = typeof value === "string" ? value : String(value);
  }
  return { ok: true, headers: out };
}

/** What the operator picked: a known upstream, or a bare protocol for an
 *  address of their own. */
export type Upstream =
  | { kind: "preset"; preset: ProviderPreset }
  | { kind: "custom"; protocol: ProtocolName; label: string };

export function upstreamKey(u: Upstream): string {
  return u.kind === "preset" ? `preset:${u.preset.label}` : `custom:${u.protocol}`;
}

function normalizeURL(url: string): string {
  return url.trim().replace(/\/+$/, "").toLowerCase();
}

/**
 * Which preset a saved provider was made from, recovered from its protocol
 * and address, so editing opens on the same card the operator picked. A
 * Standard Agent Platform address also yields the project and location it was
 * built from. Anything else is a custom upstream.
 */
export function presetFor(
  protocol: ProtocolName,
  baseURL: string,
  vendor = "",
): { preset: ProviderPreset; project: string; location: string } | null {
  if (vendor) {
    const byVendor = PRESETS.find((p) => p.vendor === vendor);
    if (byVendor) return { preset: byVendor, project: "", location: "" };
  }
  const exact = PRESETS.find(
    (p) => !p.vendor && !p.agentPlatformStandard && p.protocol === protocol && normalizeURL(p.base_url) === normalizeURL(baseURL),
  );
  if (exact) return { preset: exact, project: "", location: "" };
  const standard = PRESETS.find((p) => p.agentPlatformStandard);
  const m = /\/projects\/([^/]+)\/locations\/([^/]+)\/publishers\/google\/?$/.exec(baseURL);
  if (standard && protocol === "gemini" && isAgentPlatformBaseURL(baseURL) && m) {
    return {
      preset: standard,
      project: decodeURIComponent(m[1]),
      location: m[2] === "global" ? "" : m[2],
    };
  }
  return null;
}

/** The label of a "custom" card: the protocol's catalog name, which some
 *  entries already end in "Compatible", said once. */
export function customProtocolName(label: string): string {
  return label.replace(/\s*compatible$/i, "");
}
