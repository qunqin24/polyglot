/**
 * The name a person reads for a model.
 *
 * The models.dev catalog's own name wins when the server has one — it is the
 * maker's spelling, already tidied ("DeepSeek V4 Flash", "GPT 5.5"). An id the
 * catalog does not know — a local Ollama tag, a reseller's private alias, a
 * model newer than the snapshot — is spelled out by the rules below, so the
 * page never falls back to a raw id: "llama-3.3-70b-versatile" reads
 * "Llama 3.3 70B Versatile", "qwen3:8b" reads "Qwen3 8B".
 */
export function modelLabel(id: string, catalogName?: string | null): string {
  if (catalogName) return catalogName;
  return spellModelId(id);
}

// Brand spellings that capitalising the first letter would get wrong.
const BRANDS: Record<string, string> = {
  deepseek: "DeepSeek",
  openai: "OpenAI",
  minimax: "MiniMax",
  chatgpt: "ChatGPT",
  gpt: "GPT",
  glm: "GLM",
  ernie: "ERNIE",
  internlm: "InternLM",
  longcat: "LongCat",
  qwq: "QwQ",
  qvq: "QVQ",
  mimo: "MiMo",
  "gpt-oss": "GPT OSS",
};

// Words that are abbreviations and read in capitals.
const UPPER = new Set(["ai", "vl", "ocr", "tts", "asr", "moe", "oss", "fp8", "fp16", "bf16", "awq", "gguf", "it", "hd", "xl", "rl"]);

function spellModelId(id: string): string {
  // A reseller's "vendor/model" names the vendor twice; the model is the part
  // after the last slash.
  const bare = id.slice(id.lastIndexOf("/") + 1).trim();
  if (!bare) return id;
  if (BRANDS[bare.toLowerCase()]) return BRANDS[bare.toLowerCase()];
  const words = bare.split(/[-_:\s]+/).filter(Boolean);
  // "claude-sonnet-4-6" is version 4.6: two short numbers in a row are one
  // version split by the hyphen that ids use in place of a dot.
  const merged: string[] = [];
  for (const w of words) {
    const prev = merged[merged.length - 1];
    if (prev !== undefined && /^\d{1,2}$/.test(prev) && /^\d{1,2}$/.test(w)) {
      merged[merged.length - 1] = `${prev}.${w}`;
      continue;
    }
    merged.push(w);
  }
  return merged.map(spellWord).join(" ");
}

function spellWord(word: string): string {
  const lower = word.toLowerCase();
  if (BRANDS[lower]) return BRANDS[lower];
  if (UPPER.has(lower)) return lower.toUpperCase();
  // Parameter counts: 70b, 8b, 1.5b, a22b (active), 405m; experts as 8x7B.
  if (/^a?\d+(\.\d+)?[bmk]$/i.test(word)) return word.toUpperCase();
  if (/^\d+x\d+[bmk]$/i.test(word)) return word.slice(0, -1).toLowerCase() + word.slice(-1).toUpperCase();
  // OpenAI's o-series is spelled in lower case: "o4 mini".
  if (/^o\d/i.test(word)) return lower;
  // A letter and a version: v4, k3, r1.
  if (/^[a-np-z]\d+(\.\d+)?$/i.test(word)) return word.toUpperCase();
  // Dates and plain numbers stay as they are.
  if (/^\d/.test(word)) return word;
  // Brand glued to a version — qwen3, gemma3, phi4 — keeps its digits.
  const glued = /^([a-z]+)(\d.*)$/i.exec(word);
  if (glued && BRANDS[glued[1].toLowerCase()]) return BRANDS[glued[1].toLowerCase()] + glued[2];
  return word.charAt(0).toUpperCase() + word.slice(1);
}
