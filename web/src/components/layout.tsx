import * as React from "react";
import { NavLink } from "react-router-dom";
import {
  Activity,
  Server,
  Boxes,
  CircleDollarSign,
  KeyRound,
  ScrollText,
  Microscope,
  Settings as SettingsIcon,
  Sun,
  Moon,
  Monitor,
  LogOut,
  Menu,
  X,
  Languages,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useTheme, type Theme } from "@/lib/hooks";
import { useT, useI18n, LOCALES } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Logo } from "@/components/logo";
import { UpdateNotice } from "@/components/update-notice";
import { useSession } from "@/App";
import type { TranslationKey } from "@/lib/i18n";

const NAV: { to: string; label: TranslationKey; icon: typeof Activity; end?: boolean }[] = [
  { to: "/", label: "nav.overview", icon: Activity, end: true },
  { to: "/providers", label: "nav.providers", icon: Server },
  { to: "/models", label: "nav.models", icon: Boxes },
  { to: "/pricing", label: "nav.pricing", icon: CircleDollarSign },
  { to: "/keys", label: "nav.keys", icon: KeyRound },
  { to: "/logs", label: "nav.logs", icon: ScrollText },
  { to: "/inspector", label: "nav.inspector", icon: Microscope },
  { to: "/settings", label: "nav.settings", icon: SettingsIcon },
];

export function Layout({ children }: { children: React.ReactNode }) {
  const t = useT();
  const [mobileOpen, setMobileOpen] = React.useState(false);
  const { me, signOut } = useSession();

  return (
    // A column below the lg breakpoint so the open drawer can claim the
    // remaining height; a two-column grid above it.
    <div className="flex min-h-screen flex-col lg:grid lg:grid-cols-[15rem_1fr]">
      {/* Mobile header */}
      <header className="sticky top-0 z-30 flex items-center justify-between border-b border-border bg-background/85 px-4 py-3 backdrop-blur lg:hidden">
        <Wordmark />
        <Button variant="ghost" size="icon" onClick={() => setMobileOpen((v) => !v)}>
          {mobileOpen ? <X /> : <Menu />}
          <span className="sr-only">{t("common.menu")}</span>
        </Button>
      </header>

      <aside
        className={cn(
          // Always a column, so the account block can sit at the end of it
          // rather than trailing the last nav link.
          "flex flex-col border-r border-border bg-card/40",
          "lg:sticky lg:top-0 lg:h-screen",
          // Open on mobile, the drawer takes the height the header leaves.
          mobileOpen ? "flex-1" : "hidden lg:flex",
        )}
      >
        <div className="hidden px-5 pb-4 pt-6 lg:block">
          <Wordmark />
        </div>

        <nav className="flex flex-1 flex-col gap-0.5 p-3">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              onClick={() => setMobileOpen(false)}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground",
                )
              }
            >
              <item.icon className="size-4" />
              {t(item.label)}
            </NavLink>
          ))}
        </nav>

        <div className="flex items-center justify-between gap-2 border-t border-border p-3">
          <div className="min-w-0">
            <p className="truncate text-xs font-medium">{me.username}</p>
            <p className="truncate text-xs text-muted-foreground">v{me.version}</p>
          </div>
          <div className="flex items-center gap-1">
            <LocaleToggle />
            <ThemeToggle />
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={() => void signOut()}
              title={t("common.signOut")}
            >
              <LogOut />
              <span className="sr-only">{t("common.signOut")}</span>
            </Button>
          </div>
        </div>
      </aside>

      <main className="min-w-0 px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
        <UpdateNotice />
        {children}
      </main>
    </div>
  );
}

function Wordmark() {
  return (
    <div className="flex items-center gap-2">
      <Logo className="size-6 text-primary" />
      <div className="leading-none">
        <span className="text-[15px] font-semibold tracking-tight">Polyglot</span>
      </div>
    </div>
  );
}

function ThemeToggle() {
  const t = useT();
  const { theme, setTheme } = useTheme();
  const order: Theme[] = ["system", "light", "dark"];
  const Icon = theme === "dark" ? Moon : theme === "light" ? Sun : Monitor;

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      title={t("settings.theme")}
      onClick={() => setTheme(order[(order.indexOf(theme) + 1) % order.length])}
    >
      <Icon />
      <span className="sr-only">{t("settings.theme")}</span>
    </Button>
  );
}

// A two-language toggle is a button, not a menu. It shows the language you
// would switch to, which is the only thing worth knowing here.
function LocaleToggle() {
  const t = useT();
  const { locale, setLocale } = useI18n();
  const next = LOCALES[(LOCALES.findIndex((l) => l.value === locale) + 1) % LOCALES.length];

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      title={`${t("settings.language")}: ${next.label}`}
      onClick={() => setLocale(next.value)}
    >
      <Languages />
      <span className="sr-only">{next.label}</span>
    </Button>
  );
}

export function PageHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {action}
    </div>
  );
}
