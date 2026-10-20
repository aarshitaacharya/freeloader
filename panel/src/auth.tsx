import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { AdminClient } from "./api/client";

const STORAGE_KEY = "fl-admin-token";

/** sessionStorage can throw (private windows, blocked storage); the panel still works without it. */
function load(): string {
  try {
    return sessionStorage.getItem(STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}
function save(token: string): void {
  try {
    if (token === "") sessionStorage.removeItem(STORAGE_KEY);
    else sessionStorage.setItem(STORAGE_KEY, token);
  } catch {
    /* ignore */
  }
}

interface Auth {
  client: AdminClient | undefined;
  login: (token: string) => void;
  logout: () => void;
}

const Ctx = createContext<Auth | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState(load);
  const client = useMemo(() => (token === "" ? undefined : new AdminClient(token)), [token]);
  const login = useCallback((t: string) => {
    save(t);
    setToken(t);
  }, []);
  const logout = useCallback(() => {
    save("");
    setToken("");
  }, []);
  const value = useMemo(() => ({ client, login, logout }), [client, login, logout]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): Auth {
  const v = useContext(Ctx);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

/** The client, for pages that are only rendered once logged in. */
export function useClient(): AdminClient {
  const { client } = useAuth();
  if (!client) throw new Error("not logged in");
  return client;
}
