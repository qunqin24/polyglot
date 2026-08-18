import * as React from "react";
import { en, type Catalog, type TranslationKey } from "./en";
import { zh } from "./zh";
import { setFormatLocale } from "@/lib/utils";

// A ~70 line i18n layer instead of a dependency. Polyglot needs typed keys,
// one placeholder syntax and a locale switch; nothing else.

export type Locale = "en" | "zh";

export const LOCALES: { value: Locale; label: string; bcp47: string }[] = [
  { value: "en", label: "English", bcp47: "en" },
  { value: "zh", label: "简体中文", bcp47: "zh-CN" },
];

const CATALOGS: Record<Locale, Catalog> = { en, zh };

const STORAGE_KEY = "polyglot-locale";

/** Values substituted into {placeholders}. */
export type TVars = Record<string, string | number>;

export type TFunction = (key: TranslationKey, vars?: TVars) => string;

interface I18nValue {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: TFunction;
}

const I18nContext = React.createContext<I18nValue | null>(null);

function detectLocale(): Locale {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === "en" || stored === "zh") return stored;
  // navigator.languages is ordered by preference, so honour the first match.
  for (const tag of navigator.languages ?? [navigator.language]) {
    if (tag?.toLowerCase().startsWith("zh")) return "zh";
    if (tag?.toLowerCase().startsWith("en")) return "en";
  }
  return "en";
}

function bcp47(locale: Locale): string {
  return LOCALES.find((l) => l.value === locale)?.bcp47 ?? "en";
}

function interpolate(template: string, vars?: TVars): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in vars ? String(vars[name]) : match,
  );
}

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = React.useState<Locale>(detectLocale);

  React.useEffect(() => {
    const tag = bcp47(locale);
    document.documentElement.lang = tag;
    // Dates and numbers formatted in lib/utils follow the same locale.
    setFormatLocale(tag);
  }, [locale]);

  const setLocale = React.useCallback((l: Locale) => {
    localStorage.setItem(STORAGE_KEY, l);
    setLocaleState(l);
  }, []);

  const value = React.useMemo<I18nValue>(() => {
    const catalog = CATALOGS[locale];
    return {
      locale,
      setLocale,
      t: (key, vars) => interpolate(catalog[key] ?? en[key] ?? key, vars),
    };
  }, [locale, setLocale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const ctx = React.useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used inside <I18nProvider>");
  return ctx;
}

/** Shorthand for components that only need to translate. */
export function useT(): TFunction {
  return useI18n().t;
}

export type { TranslationKey };
