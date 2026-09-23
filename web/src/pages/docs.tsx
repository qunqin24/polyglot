import * as React from "react";
import { Link, Navigate, NavLink, useLocation } from "react-router-dom";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { ArrowUpRight, Languages } from "lucide-react";
import { Logo } from "@/components/logo";
import { useI18n, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import readmeEn from "../../../README.md?raw";
import readmeZh from "../../../README.zh-CN.md?raw";
import apiEn from "../../../docs/api.md?raw";
import apiZh from "../../../docs/api.zh-CN.md?raw";
import logApi from "../../../docs/log-api.md?raw";
import development from "../../../AGENTS.md?raw";
import performance from "../../../docs/webui-performance.md?raw";

type DocID = "overview" | "api" | "log-api" | "development" | "performance";

const DOCUMENTS: { id: DocID; path: string; label: TranslationKey }[] = [
  { id: "overview", path: "/docs", label: "docs.overview" },
  { id: "api", path: "/docs/api", label: "docs.api" },
  { id: "log-api", path: "/docs/log-api", label: "docs.logApi" },
  { id: "development", path: "/docs/development", label: "docs.development" },
  { id: "performance", path: "/docs/performance", label: "docs.performance" },
];

const screenshots = import.meta.glob<string>("../../../docs/screenshots/*.png", {
  eager: true,
  query: "?url",
  import: "default",
});

function slug(text: string): string {
  return text.toLowerCase().trim().replace(/[^\p{L}\p{N}\s-]/gu, "").replace(/\s+/g, "-");
}

function headingText(node: React.ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(headingText).join("");
  if (React.isValidElement<{ children?: React.ReactNode }>(node)) return headingText(node.props.children);
  return "";
}

function docBody(id: DocID, locale: "en" | "zh"): string {
  if (id === "overview") {
    // README's GitHub-only wrapper is presentation, not documentation. The
    // same source text remains the single source of truth for both languages.
    return (locale === "zh" ? readmeZh : readmeEn)
      .replace(/^<div align="center">\s*$/m, "")
      .replace(/^<img src="web\/public\/favicon.svg"[^>]*>\s*$/m, "")
      .replace(/^<\/div>\s*$/m, "");
  }
  if (id === "log-api") return logApi;
  if (id === "api") return locale === "zh" ? apiZh : apiEn;
  if (id === "development") return development;
  return performance;
}

export default function DocsPage() {
  const { locale, setLocale, t } = useI18n();
  const location = useLocation();
  const path = location.pathname.replace(/\/$/, "") || "/docs";
  const selected = DOCUMENTS.find((doc) => doc.path === path);

  React.useEffect(() => {
    if (!location.hash) window.scrollTo(0, 0);
  }, [location.pathname, location.hash]);

  if (!selected) return <Navigate to="/docs" replace />;

  const body = docBody(selected.id, locale);
  const headings = [...body.matchAll(/^## (.+)$/gm)].map((match) => ({
    title: match[1],
    id: slug(match[1]),
  }));

  const components: Components = {
    h1: ({ children }) => <h1>{children}</h1>,
    h2: ({ children }) => <h2 id={slug(headingText(children))}>{children}</h2>,
    h3: ({ children }) => <h3 id={slug(headingText(children))}>{children}</h3>,
    a: ({ href, children }) => {
      if (href === "README.md" || href === "README.zh-CN.md" || href === "../README.md" || href === "../README.zh-CN.md") {
        return (
          <Link
            className="docs-text-link"
            to="/docs"
            onClick={() => setLocale(href.endsWith("README.md") ? "en" : "zh")}
          >{children}</Link>
        );
      }
      const internal: Record<string, string> = {
        "docs/api.md": "/docs/api",
        "docs/api.zh-CN.md": "/docs/api",
        "log-api.md": "/docs/log-api",
        "docs/log-api.md": "/docs/log-api",
        "docs/webui-performance.md": "/docs/performance",
        "AGENTS.md": "/docs/development",
      };
      if (href && internal[href]) return <Link className="docs-text-link" to={internal[href]}>{children}</Link>;
      if (href === "LICENSE") {
        return <a className="docs-text-link" href="https://github.com/qunqin24/polyglot/blob/main/LICENSE">{children}</a>;
      }
      return <a className="docs-text-link" href={href}>{children}</a>;
    },
    img: ({ src, alt }) => {
      const image = src ? screenshots[`../../../${src}`] : undefined;
      return image ? <img src={image} alt={alt ?? ""} loading="lazy" className="my-6 w-full rounded-lg border border-border" /> : null;
    },
    table: ({ children }) => <div className="my-5 overflow-x-auto rounded-md border border-border"><table>{children}</table></div>,
    pre: ({ children }) => <pre>{children}</pre>,
  };

  const untranslated = (selected.id === "log-api" && locale === "zh") ||
    ((selected.id === "development" || selected.id === "performance") && locale === "en");

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-20 border-b border-border bg-background/95 backdrop-blur">
        <div className="mx-auto flex h-15 max-w-[94rem] items-center justify-between gap-4 px-5 sm:px-8">
          <Link to="/docs" className="flex items-center gap-2.5 font-semibold tracking-tight text-foreground">
            <Logo className="size-6 text-primary" />
            <span>Polyglot</span>
            <span className="ml-1 border-l border-border pl-3 text-sm font-normal text-muted-foreground">{t("docs.title")}</span>
          </Link>
          <div className="flex items-center gap-4 text-sm">
            <button
              type="button"
              className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
              onClick={() => setLocale(locale === "en" ? "zh" : "en")}
              aria-label={t("settings.language")}
            >
              <Languages className="size-4" />
              <span>{locale === "en" ? "简体中文" : "English"}</span>
            </button>
            <Link to="/" className="hidden items-center gap-1 text-muted-foreground hover:text-foreground sm:inline-flex">
              {t("docs.dashboard")} <ArrowUpRight className="size-3.5" />
            </Link>
          </div>
        </div>
      </header>

      <div className="mx-auto grid max-w-[94rem] grid-cols-[minmax(0,1fr)] gap-8 px-5 py-8 sm:px-8 lg:grid-cols-[12.5rem_minmax(0,1fr)] lg:gap-12 xl:grid-cols-[12.5rem_minmax(0,1fr)_12rem]">
        <nav aria-label={t("docs.title")} className="flex min-w-0 gap-1 overflow-x-auto border-b border-border pb-4 lg:sticky lg:top-23 lg:h-fit lg:flex-col lg:overflow-visible lg:border-b-0 lg:pb-0">
          {DOCUMENTS.map((doc) => (
            <NavLink
              key={doc.id}
              to={doc.path}
              end
              className={({ isActive }) => cn(
                "whitespace-nowrap rounded-md px-3 py-2 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
                isActive ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              {t(doc.label)}
            </NavLink>
          ))}
        </nav>

        <main className="min-w-0 pb-20">
          {untranslated && <p className="mb-5 text-xs text-muted-foreground">{t("docs.otherLanguage")}</p>}
          {headings.length > 0 && (
            <details className="mb-8 rounded-lg border border-border px-4 py-3 text-sm xl:hidden">
              <summary className="cursor-pointer font-medium">{t("docs.contents")}</summary>
              <ul className="mt-3 grid gap-x-5 gap-y-2 border-t border-border pt-3 sm:grid-cols-2">
                {headings.map((heading) => (
                  <li key={heading.id}>
                    <a href={`#${heading.id}`} className="text-muted-foreground hover:text-foreground">{heading.title}</a>
                  </li>
                ))}
              </ul>
            </details>
          )}
          <article className="docs-prose max-w-[48rem]">
            <Markdown remarkPlugins={[remarkGfm]} components={components}>{body}</Markdown>
          </article>
        </main>

        {headings.length > 0 && (
          <aside className="hidden xl:block">
            <nav aria-label={t("docs.contents")} className="sticky top-23 max-h-[calc(100vh-7rem)] overflow-y-auto">
              <p className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">{t("docs.contents")}</p>
              <ul className="space-y-1 border-l border-border">
                {headings.map((heading) => (
                  <li key={heading.id}>
                    <a href={`#${heading.id}`} className="block border-l border-transparent py-1 pl-3 -ml-px text-xs leading-5 text-muted-foreground hover:border-primary hover:text-foreground">
                      {heading.title}
                    </a>
                  </li>
                ))}
              </ul>
            </nav>
          </aside>
        )}
      </div>
    </div>
  );
}
