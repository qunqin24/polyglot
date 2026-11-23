import * as React from "react";
import { api, type Me } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { clearQueries } from "@/lib/query-cache";
import { setFormatTimeZone } from "@/lib/utils";

export type SessionState =
  | { state: "loading" }
  | { state: "setup" }
  | { state: "anonymous" }
  | { state: "signed-in"; me: Me };

interface SessionValue {
  session: SessionState;
  reload: () => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionCtx = React.createContext<SessionValue | null>(null);

/** Loads who is signed in once for the whole app; routes decide what to show. */
export function SessionProvider({ children }: { children: React.ReactNode }) {
  const [session, setSession] = React.useState<SessionState>({ state: "loading" });

  const reload = React.useCallback(async () => {
    try {
      const [status, identity] = await Promise.allSettled([api.setupStatus(), api.me()]);
      if (status.status === "fulfilled" && status.value.needs_setup) {
        setSession({ state: "setup" });
        return;
      }
      if (identity.status === "rejected") throw identity.reason;
      // Apply before the first render that shows a timestamp.
      setFormatTimeZone(identity.value.timezone);
      setSession({ state: "signed-in", me: identity.value });
    } catch {
      setSession({ state: "anonymous" });
    }
  }, []);

  React.useEffect(() => {
    void reload();
  }, [reload]);

  const signOut = React.useCallback(async () => {
    clearQueries();
    try {
      await api.logout();
    } catch (e) {
      console.warn("sign out:", errorMessage(e));
    }
    setSession({ state: "anonymous" });
  }, []);

  const value = React.useMemo(() => ({ session, reload, signOut }), [session, reload, signOut]);
  return <SessionCtx.Provider value={value}>{children}</SessionCtx.Provider>;
}

export function useSessionState(): SessionValue {
  const ctx = React.useContext(SessionCtx);
  if (!ctx) throw new Error("useSessionState must be used inside SessionProvider");
  return ctx;
}

export interface SessionContext {
  me: Me;
  refresh: () => void;
  signOut: () => Promise<void>;
}

/** The signed-in administrator. Only valid inside the authenticated routes. */
export function useSession(): SessionContext {
  const { session, reload, signOut } = useSessionState();
  if (session.state !== "signed-in") throw new Error("useSession must be used inside a signed-in route");
  return { me: session.me, refresh: () => void reload(), signOut };
}
