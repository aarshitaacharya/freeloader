import { useEffect, useState } from "react";
import { AuthProvider, useAuth } from "./auth";
import { Login } from "./components/Login";
import { Apps } from "./pages/Apps";
import { Providers } from "./pages/Providers";
import { Usage } from "./pages/Usage";

const PAGES = { apps: "Apps", usage: "Usage", providers: "Providers" } as const;
type Page = keyof typeof PAGES;

const isPage = (s: string): s is Page => s in PAGES;
const fromHash = (): Page => {
  const h = window.location.hash.replace(/^#\/?/, "");
  return isPage(h) ? h : "apps";
};

function Shell() {
  const { client, logout } = useAuth();
  const [page, setPage] = useState<Page>(fromHash);
  useEffect(() => {
    const on = () => { setPage(fromHash()); };
    window.addEventListener("hashchange", on);
    return () => { window.removeEventListener("hashchange", on); };
  }, []);

  if (!client) return <Login />;
  return (
    <div className="shell">
      <header className="topbar">
        <h1>Freeloader</h1>
        <nav aria-label="Main">
          {(Object.keys(PAGES) as Page[]).map((p) => (
            <a key={p} href={`#/${p}`} aria-current={page === p ? "page" : undefined}>{PAGES[p]}</a>
          ))}
        </nav>
        <button type="button" onClick={logout}>Sign out</button>
      </header>
      <main>
        {page === "apps" && <Apps />}
        {page === "usage" && <Usage />}
        {page === "providers" && <Providers />}
      </main>
    </div>
  );
}

export function App() {
  return (
    <AuthProvider>
      <Shell />
    </AuthProvider>
  );
}
