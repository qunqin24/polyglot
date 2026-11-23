import ai2 from "@lobehub/icons-static-svg/icons/ai2-color.svg";
import ai21 from "@lobehub/icons-static-svg/icons/ai21.svg";
import alephalpha from "@lobehub/icons-static-svg/icons/alephalpha.svg";
import arcee from "@lobehub/icons-static-svg/icons/arcee-color.svg";
import baai from "@lobehub/icons-static-svg/icons/baai.svg";
import baichuan from "@lobehub/icons-static-svg/icons/baichuan-color.svg";
import bfl from "@lobehub/icons-static-svg/icons/bfl.svg";
import bytedance from "@lobehub/icons-static-svg/icons/bytedance-color.svg";
import claude from "@lobehub/icons-static-svg/icons/claude-color.svg";
import cohere from "@lobehub/icons-static-svg/icons/cohere-color.svg";
import deepseek from "@lobehub/icons-static-svg/icons/deepseek-color.svg";
import doubao from "@lobehub/icons-static-svg/icons/doubao-color.svg";
import elevenlabs from "@lobehub/icons-static-svg/icons/elevenlabs.svg";
import essentialai from "@lobehub/icons-static-svg/icons/essentialai-color.svg";
import fireworks from "@lobehub/icons-static-svg/icons/fireworks-color.svg";
import gemini from "@lobehub/icons-static-svg/icons/gemini-color.svg";
import gemma from "@lobehub/icons-static-svg/icons/gemma-color.svg";
import grok from "@lobehub/icons-static-svg/icons/grok.svg";
import huawei from "@lobehub/icons-static-svg/icons/huawei-color.svg";
import hunyuan from "@lobehub/icons-static-svg/icons/hunyuan-color.svg";
import ibm from "@lobehub/icons-static-svg/icons/ibm.svg";
import inception from "@lobehub/icons-static-svg/icons/inception.svg";
import internlm from "@lobehub/icons-static-svg/icons/internlm-color.svg";
import kimi from "@lobehub/icons-static-svg/icons/kimi.svg";
import liquid from "@lobehub/icons-static-svg/icons/liquid.svg";
import longcat from "@lobehub/icons-static-svg/icons/longcat-color.svg";
import meta from "@lobehub/icons-static-svg/icons/meta-color.svg";
import microsoft from "@lobehub/icons-static-svg/icons/microsoft-color.svg";
import minimax from "@lobehub/icons-static-svg/icons/minimax-color.svg";
import mistral from "@lobehub/icons-static-svg/icons/mistral-color.svg";
import morph from "@lobehub/icons-static-svg/icons/morph-color.svg";
import nousresearch from "@lobehub/icons-static-svg/icons/nousresearch.svg";
import nova from "@lobehub/icons-static-svg/icons/nova-color.svg";
import nvidia from "@lobehub/icons-static-svg/icons/nvidia-color.svg";
import openai from "@lobehub/icons-static-svg/icons/openai.svg";
import perplexity from "@lobehub/icons-static-svg/icons/perplexity-color.svg";
import poolside from "@lobehub/icons-static-svg/icons/poolside-color.svg";
import qwen from "@lobehub/icons-static-svg/icons/qwen-color.svg";
import relace from "@lobehub/icons-static-svg/icons/relace.svg";
import stepfun from "@lobehub/icons-static-svg/icons/stepfun-color.svg";
import upstage from "@lobehub/icons-static-svg/icons/upstage-color.svg";
import voyage from "@lobehub/icons-static-svg/icons/voyage-color.svg";
import wenxin from "@lobehub/icons-static-svg/icons/wenxin-color.svg";
import xiaomimimo from "@lobehub/icons-static-svg/icons/xiaomimimo.svg";
import yi from "@lobehub/icons-static-svg/icons/yi-color.svg";
import zhipu from "@lobehub/icons-static-svg/icons/zhipu-color.svg";
import { SvgMark } from "@/components/brand-icon";
import { cn } from "@/lib/utils";

type Mark = { src: string; mono: boolean };

const color = (src: string): Mark => ({ src, mono: false });
const mono = (src: string): Mark => ({ src, mono: true });

/**
 * models.dev maker id → mark. The server works out the maker from the
 * catalog's canonical_model_id, so "glm-5.2" served by Alibaba still arrives
 * as "zhipuai".
 *
 * Where a company's models carry a brand of their own — Claude, Gemini, Qwen,
 * Kimi — that brand is the mark, since it is what people recognise the model
 * by. Otherwise it is the company's logo.
 *
 * A coloured file is only usable if its main shape is not white: Kimi's
 * coloured mark draws its K in #fff for a dark tile and vanishes on a light
 * card, so Kimi uses the mono file, which follows the text colour.
 */
const MAKERS: Record<string, Mark> = {
  openai: mono(openai),
  anthropic: color(claude),
  google: color(gemini),
  xai: mono(grok),
  deepseek: color(deepseek),
  alibaba: color(qwen),
  moonshotai: mono(kimi),
  zhipuai: color(zhipu),
  minimax: color(minimax),
  mistral: color(mistral),
  meta: color(meta),
  cohere: color(cohere),
  perplexity: color(perplexity),
  stepfun: color(stepfun),
  xiaomi: mono(xiaomimimo),
  nvidia: color(nvidia),
  microsoft: color(microsoft),
  amazon: color(nova),
  tencent: color(hunyuan),
  baidu: color(wenxin),
  "bytedance-seed": color(bytedance),
  meituan: color(longcat),
  huawei: color(huawei),
  baichuan: color(baichuan),
  upstage: color(upstage),
  poolside: color(poolside),
  inception: mono(inception),
  morph: color(morph),
  ai21: mono(ai21),
  allenai: color(ai2),
  "aleph-alpha": mono(alephalpha),
  "arcee-ai": color(arcee),
  baai: mono(baai),
  "black-forest-labs": mono(bfl),
  elevenlabs: mono(elevenlabs),
  essentialai: color(essentialai),
  fireworks: color(fireworks),
  ibm: mono(ibm),
  liquid: mono(liquid),
  nousresearch: mono(nousresearch),
  relace: mono(relace),
  voyageai: color(voyage),
};

/**
 * Fallback for ids the catalog does not know — a dated snapshot, a local
 * Ollama tag, a model too new for models.dev. First match wins, and order
 * matters where one family's id names another: "deepseek-r1-distill-qwen" is
 * a DeepSeek model, so DeepSeek is checked before Qwen and Llama.
 */
const RULES: [RegExp, Mark][] = [
  [/deepseek/, color(deepseek)],
  [/claude/, color(claude)],
  [/gemma/, color(gemma)],
  [/gemini/, color(gemini)],
  [/(^|[/:])(gpt|chatgpt|o[1-9]|codex|dall-e|whisper|tts-|text-embedding)/, mono(openai)],
  [/qwen|qwq|qvq/, color(qwen)],
  [/llama/, color(meta)],
  [/mistral|mixtral|codestral|magistral|devstral|pixtral|ministral|voxtral/, color(mistral)],
  [/grok/, mono(grok)],
  [/kimi|moonshot/, mono(kimi)],
  [/glm/, color(zhipu)],
  [/minimax/, color(minimax)],
  [/doubao/, color(doubao)],
  [/(^|\/)seed/, color(bytedance)],
  [/(^|\/)command|(^|\/)aya/, color(cohere)],
  [/nemotron/, color(nvidia)],
  [/(^|\/)phi-/, color(microsoft)],
  [/(^|\/)yi-/, color(yi)],
  [/hunyuan/, color(hunyuan)],
  [/ernie/, color(wenxin)],
  [/(^|\/)step-/, color(stepfun)],
  [/internlm/, color(internlm)],
  [/sonar/, color(perplexity)],
  [/longcat/, color(longcat)],
  [/mimo/, mono(xiaomimimo)],
];

function markOf(id: string, maker?: string): Mark | undefined {
  const s = id.toLowerCase();
  // Google makes both; Gemma has a mark of its own.
  if (maker === "google" && s.includes("gemma")) return color(gemma);
  if (maker && MAKERS[maker]) return MAKERS[maker];
  return RULES.find(([re]) => re.test(s))?.[1];
}

/**
 * The model's mark in a square tile. With no mark to show, the first letter
 * of the model's own name (after any "vendor/" prefix) stands in, so every
 * card still has something in that spot.
 */
export function ModelIcon({
  id,
  maker,
  className,
}: {
  id: string;
  maker?: string;
  className?: string;
}) {
  const mark = markOf(id, maker);
  const name = id.slice(id.lastIndexOf("/") + 1);
  return (
    <div
      aria-hidden
      className={cn(
        "flex size-10 shrink-0 items-center justify-center rounded-lg border bg-muted/40",
        className,
      )}
    >
      {mark ? (
        <SvgMark src={mark.src} mono={mark.mono} className="size-6" />
      ) : (
        <span className="text-sm font-semibold text-muted-foreground uppercase">
          {name.charAt(0) || "?"}
        </span>
      )}
    </div>
  );
}
