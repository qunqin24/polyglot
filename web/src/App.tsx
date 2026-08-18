import * as React from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { api, type Me } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { setFormatTimeZone } from "@/lib/utils";
import { Spinner } from "@/components/ui/misc";
import { Layout } from "@/components/layout";
import { AuthPage } from "@/pages/auth";
import { Overview } from "@/pages/overview";
import { Providers } from "@/pages/providers";
import { Models } from "@/pages/models";
import { Pricing } from "@/pages/pricing";
import { Keys } from "@/pages/keys";
import { Logs } from "@/pages/logs";
import { Inspector } from "@/pages/inspector";
import { Settings } from "@/pages/settings";

type Session =
  | { state: "loading" }
  | { state: "setup" }
  | { state: "anonymous" }
  | { state: "signed-in"; me: Me };

export interface SessionContext {
  me: Me;
  refresh: () => void;
  signOut: () => Promise<void>;
}

export const SessionCtx = React.createContext<SessionContext | null>(null);

export function useSession(): SessionContext {
  const ctx = React.useContext(SessionCtx);
  if (!ctx) throw new Error("useSession must be used inside a signed-in route");
  return ctx;
}

export function App() {
  const [session, setSession] = React.useState<Session>({ state: "loading" });

  const load = React.useCallback(async () => {
    try {
      const status = await api.setupStatus();
      if (status.needs_setup) {
        setSession({ state: "setup" });
        return;
      }
      const me = await api.me();
      // Apply before the first render that shows a timestamp, so nothing is
      // painted in the wrong zone and then corrected.
      setFormatTimeZone(me.timezone);
      setSession({ state: "signed-in", me });
    } catch {
      setSession({ state: "anonymous" });
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const signOut = React.useCallback(async () => {
    try {
      await api.logout();
    } catch (e) {
      console.warn("sign out:", errorMessage(e));
    }
    setSession({ state: "anonymous" });
  }, []);

  if (session.state === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner className="size-5 text-muted-foreground" />
      </div>
    );
  }

  if (session.state !== "signed-in") {
    return <AuthPage mode={session.state === "setup" ? "setup" : "login"} onDone={() => void load()} />;
  }

  const ctx: SessionContext = { me: session.me, refresh: () => void load(), signOut };

  return (
    <SessionCtx.Provider value={ctx}>
      <Layout>
        <Routes>
          <Route path="/" element={<Overview />} />
          <Route path="/providers" element={<Providers />} />
          <Route path="/models" element={<Models />} />
          <Route path="/pricing" element={<Pricing />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/inspector" element={<Inspector />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
    </SessionCtx.Provider>
  );
}
