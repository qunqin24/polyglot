import * as React from "react";
import { api } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { browserTimeZone } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { ErrorBanner, Spinner } from "@/components/ui/misc";
import { Logo } from "@/components/logo";

// First run creates the single local administrator; after that this is the
// sign-in form. There is no registration and no password reset by design.
export function AuthPage({ mode, onDone }: { mode: "setup" | "login"; onDone: () => void }) {
  const t = useT();
  const [username, setUsername] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [setupToken, setSetupToken] = React.useState("");
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const isSetup = mode === "setup";

  // Detected once, not on every render: the zone cannot change mid-form, and a
  // stable value keeps the hint below from flickering.
  const [timezone] = React.useState(browserTimeZone);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (isSetup && password !== confirm) {
      setError(t("auth.passwordMismatch"));
      return;
    }
    setBusy(true);
    try {
      if (isSetup) await api.setup(username, password, timezone, setupToken);
      else await api.login(username, password);
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="mb-4 flex justify-center">
            <Logo className="size-10 text-primary" />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {isSetup ? t("auth.setupTitle") : t("auth.loginTitle")}
          </h1>
          <p className="mt-1.5 text-sm text-muted-foreground">
            {isSetup ? t("auth.setupSubtitle") : t("auth.loginSubtitle")}
          </p>
        </div>

        <form onSubmit={(e) => void submit(e)} className="space-y-4">
          {isSetup && (
            <Field label={t("auth.setupToken")} hint={t("auth.setupTokenHint")}>
              <Input
                type="password"
                value={setupToken}
                onChange={(e) => setSetupToken(e.target.value)}
                autoComplete="off"
                autoFocus
                required
              />
            </Field>
          )}

          <Field label={t("auth.username")}>
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus={!isSetup}
              required
            />
          </Field>

          <Field label={t("auth.password")} hint={isSetup ? t("auth.passwordHint") : undefined}>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete={isSetup ? "new-password" : "current-password"}
              required
              minLength={isSetup ? 8 : undefined}
            />
          </Field>

          {isSetup && (
            <Field label={t("auth.confirmPassword")}>
              <Input
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                required
              />
            </Field>
          )}

          {isSetup && (
            <p className="text-xs text-muted-foreground">
              {t("auth.timezoneDetected", { timezone })}
            </p>
          )}

          <ErrorBanner message={error} />

          <Button type="submit" className="w-full" disabled={busy}>
            {busy && <Spinner />}
            {isSetup ? t("auth.createAdmin") : t("auth.signIn")}
          </Button>
        </form>

        {isSetup && (
          <p className="mt-6 text-center text-xs text-muted-foreground">
            {t("auth.nextSteps")}
          </p>
        )}
      </div>
    </div>
  );
}
