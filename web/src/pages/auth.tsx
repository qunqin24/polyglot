import * as React from "react";
import { Link } from "@tanstack/react-router";
import { api } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { browserTimeZone } from "@/lib/utils";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Field } from "@/components/kit/input";
import { ErrorBanner } from "@/components/kit/misc";
import { LoadingButton } from "@/components/kit/loading-button";
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
    <div className="container grid h-svh max-w-none items-center justify-center">
      <div className="mx-auto flex w-full flex-col justify-center space-y-2 py-8 sm:w-[400px] sm:p-8">
        <div className="mb-4 flex items-center justify-center">
          <Logo className="me-2 size-6 text-primary" />
          <h1 className="text-xl font-medium">Polyglot</h1>
        </div>
        <Card className="gap-4">
          <CardHeader>
            <CardTitle className="text-lg tracking-tight">
              {isSetup ? t("auth.setupTitle") : t("auth.loginTitle")}
            </CardTitle>
            <CardDescription>
              {isSetup ? t("auth.setupSubtitle") : t("auth.loginSubtitle")}
            </CardDescription>
          </CardHeader>
          <CardContent>
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

              <LoadingButton type="submit" className="w-full" busy={busy}>
                {isSetup ? t("auth.createAdmin") : t("auth.signIn")}
              </LoadingButton>
            </form>
          </CardContent>
          <CardFooter className="flex-col gap-3">
            {isSetup && (
              <p className="text-center text-xs text-muted-foreground">{t("auth.nextSteps")}</p>
            )}
            <Link to="/docs/$" params={{ _splat: "" }} className="text-sm text-muted-foreground underline underline-offset-4 hover:text-primary">
              {t("auth.docsLink")}
            </Link>
          </CardFooter>
        </Card>
      </div>
    </div>
  );
}
