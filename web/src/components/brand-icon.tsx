import anthropic from "@lobehub/icons-static-svg/icons/anthropic.svg";
import deepseek from "@lobehub/icons-static-svg/icons/deepseek-color.svg";
import gemini from "@lobehub/icons-static-svg/icons/gemini-color.svg";
import groq from "@lobehub/icons-static-svg/icons/groq.svg";
import ollama from "@lobehub/icons-static-svg/icons/ollama.svg";
import openai from "@lobehub/icons-static-svg/icons/openai.svg";
import openrouter from "@lobehub/icons-static-svg/icons/openrouter.svg";
import siliconcloud from "@lobehub/icons-static-svg/icons/siliconcloud-color.svg";
import vertexai from "@lobehub/icons-static-svg/icons/vertexai-color.svg";
import { cn } from "@/lib/utils";

/**
 * Vendor marks, so a list of upstreams can be scanned instead of read.
 *
 * The files come from @lobehub/icons-static-svg — plain SVG files with no
 * dependencies. Its React sibling, @lobehub/icons, peer-depends on antd and
 * @lobehub/ui, which is a second UI framework for the sake of nine logos.
 *
 * Each file is imported individually, so the bundle carries these nine and
 * not the other 894 in the package.
 */
const BRANDS = {
  openai: { src: openai, mono: true },
  anthropic: { src: anthropic, mono: true },
  openrouter: { src: openrouter, mono: true },
  groq: { src: groq, mono: true },
  ollama: { src: ollama, mono: true },
  gemini: { src: gemini, mono: false },
  vertexai: { src: vertexai, mono: false },
  deepseek: { src: deepseek, mono: false },
  siliconcloud: { src: siliconcloud, mono: false },
} as const;

export type BrandName = keyof typeof BRANDS;

/**
 * A brand mark at text size.
 *
 * Marks that have no colour of their own — OpenAI, Anthropic, Groq, Ollama,
 * OpenRouter — are drawn as a mask filled with the current text colour. The
 * files say `fill="currentColor"`, but an <img> is its own document and
 * resolves that to black, which is invisible on a dark sheet.
 *
 * Decorative in every use so far: the vendor's name is always next to it, so
 * the mark is hidden from screen readers rather than repeating the label.
 */
export function BrandIcon({ name, className }: { name: BrandName; className?: string }) {
  const brand = BRANDS[name];
  if (brand.mono) {
    return (
      <span
        aria-hidden
        className={cn("inline-block size-4 shrink-0 bg-current", className)}
        style={{
          maskImage: `url("${brand.src}")`,
          WebkitMaskImage: `url("${brand.src}")`,
          maskSize: "contain",
          WebkitMaskSize: "contain",
          maskRepeat: "no-repeat",
          WebkitMaskRepeat: "no-repeat",
          maskPosition: "center",
          WebkitMaskPosition: "center",
        }}
      />
    );
  }
  return <img src={brand.src} alt="" aria-hidden className={cn("size-4 shrink-0", className)} />;
}
