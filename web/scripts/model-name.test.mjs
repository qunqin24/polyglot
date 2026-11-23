import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/model-name.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
});
const { modelLabel } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`
);

// The catalog's name always wins.
assert.equal(modelLabel("deepseek-v4-flash", "DeepSeek V4 Flash"), "DeepSeek V4 Flash");

// Ids the catalog does not know are spelled out, never shown raw.
const cases = {
  "deepseek-v4-flash": "DeepSeek V4 Flash",
  "llama-3.3-70b-versatile": "Llama 3.3 70B Versatile",
  "qwen3:8b": "Qwen3 8B",
  "qwen3-235b-a22b": "Qwen3 235B A22B",
  "meta-llama/Llama-3.1-8B-Instruct": "Llama 3.1 8B Instruct",
  "gpt-5.5": "GPT 5.5",
  "gpt-oss-120b": "GPT OSS 120B",
  "o4-mini": "o4 Mini",
  "glm-5.2": "GLM 5.2",
  "kimi-k3": "Kimi K3",
  "deepseek-r1-distill-qwen-32b": "DeepSeek R1 Distill Qwen 32B",
  "minimax-m3": "MiniMax M3",
  "gemma3:27b-it-qat": "Gemma3 27B IT Qat",
  "mixtral-8x7b": "Mixtral 8x7B",
  "claude-sonnet-4-6-20260101": "Claude Sonnet 4.6 20260101",
};
for (const [id, want] of Object.entries(cases)) {
  assert.equal(modelLabel(id), want, id);
}
console.log("model-name: ok");
