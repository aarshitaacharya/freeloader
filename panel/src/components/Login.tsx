import { useState, type SyntheticEvent } from "react";
import { AdminClient, ApiError } from "../api/client";
import { useAuth } from "../auth";

export function Login() {
  const { login } = useAuth();
  const [token, setToken] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  async function submit(e: SyntheticEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await new AdminClient(token.trim()).listApps(); // the cheapest authenticated call
      login(token.trim());
    } catch (err) {
      if (err instanceof ApiError && err.unauthorized) setError("That admin token was not accepted.");
      else if (err instanceof ApiError && err.status === 404) setError("The admin API is not enabled on this gateway (no ADMIN_TOKEN set).");
      else setError(err instanceof Error ? err.message : "Could not reach the gateway.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="login">
      <form onSubmit={(e) => void submit(e)} className="card">
        <h1>Freeloader</h1>
        <p className="muted">Enter the gateway&apos;s admin token.</p>
        <label>
          Admin token
          <input type="password" value={token} onChange={(e) => { setToken(e.target.value); }} autoFocus autoComplete="off" />
        </label>
        {error !== undefined && <p role="alert" className="error">{error}</p>}
        <button type="submit" className="primary" disabled={busy || token.trim() === ""}>
          {busy ? "Checking…" : "Sign in"}
        </button>
      </form>
    </main>
  );
}
