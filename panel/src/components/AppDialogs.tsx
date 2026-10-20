import { useEffect, useState, type SyntheticEvent, type ReactNode } from "react";
import { ApiError } from "../api/client";
import type { App, AppPatch, NewApp } from "../api/schemas";

function Dialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); };
  }, [onClose]);
  return (
    <div className="overlay">
      <div className="dialog" role="dialog" aria-modal="true" aria-label={title}>
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}

/** "" -> 0 is NOT accepted silently: an empty field is an error, 0 must be typed. */
function parseCount(s: string): number | undefined {
  return /^\d+$/.test(s.trim()) ? Number(s.trim()) : undefined;
}

/** What the dialog hands back: a create and an update are different requests. */
export type SaveRequest = { kind: "create"; values: NewApp } | { kind: "update"; id: number; values: AppPatch };

interface AppDialogProps {
  existing: App | undefined;
  providers: readonly string[];
  onSave: (result: SaveRequest) => Promise<void>;
  onClose: () => void;
}

export function AppDialog({ existing, providers, onSave, onClose }: AppDialogProps) {
  const [name, setName] = useState(existing?.name ?? "");
  const [rpm, setRpm] = useState(String(existing?.rpm_limit ?? 60));
  const [tokens, setTokens] = useState(String(existing?.tokens_per_day ?? 100000));
  const [ttl, setTtl] = useState(String(existing?.cache_ttl_s ?? 86400));
  const [priority, setPriority] = useState<string[]>(existing?.provider_priority ?? []);
  const [cacheAll, setCacheAll] = useState(existing?.cache_all ?? false);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  async function submit(e: SyntheticEvent) {
    e.preventDefault();
    const r = parseCount(rpm), t = parseCount(tokens), c = parseCount(ttl);
    if (existing === undefined && name.trim() === "") {
      setError("Give the app a name.");
      return;
    }
    if (r === undefined || t === undefined || c === undefined) {
      setError("Limits must be whole numbers, 0 or more.");
      return;
    }
    setBusy(true);
    setError(undefined);
    const values = { rpm_limit: r, tokens_per_day: t, cache_ttl_s: c, provider_priority: priority, cache_all: cacheAll };
    try {
      await onSave(existing ? { kind: "update", id: existing.id, values } : { kind: "create", values: { name: name.trim(), ...values } });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  const toggle = (p: string) => { setPriority((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p])); };

  return (
    <Dialog title={existing ? `Edit ${existing.name}` : "New app"} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)}>
        {!existing && (
          <label>Name<input value={name} onChange={(e) => { setName(e.target.value); }} maxLength={64} autoFocus /></label>
        )}
        <div className="row">
          <label>Requests per minute<input inputMode="numeric" value={rpm} onChange={(e) => { setRpm(e.target.value); }} /></label>
          <label>Tokens per day<input inputMode="numeric" value={tokens} onChange={(e) => { setTokens(e.target.value); }} /></label>
          <label>Cache TTL (seconds)<input inputMode="numeric" value={ttl} onChange={(e) => { setTtl(e.target.value); }} /></label>
        </div>
        <p className="muted small">0 means unlimited (cache TTL 0 turns response caching off).</p>
        <fieldset>
          <legend>Provider order</legend>
          <p className="muted small">Click to add providers in the order they should be tried first. None selected = each alias&apos;s default order.</p>
          <div className="chips">
            {providers.map((p) => {
              const pos = priority.indexOf(p);
              return (
                <button type="button" key={p} className={pos >= 0 ? "chip on" : "chip"} aria-pressed={pos >= 0} onClick={() => { toggle(p); }}>
                  {pos >= 0 && <span className="chip-pos">{pos + 1}</span>}{p}
                </button>
              );
            })}
          </div>
        </fieldset>
        <label className="check">
          <input type="checkbox" checked={cacheAll} onChange={(e) => { setCacheAll(e.target.checked); }} />
          Also cache requests whose temperature is not 0
        </label>
        {error !== undefined && <p role="alert" className="error">{error}</p>}
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>Cancel</button>
          <button type="submit" className="primary" disabled={busy}>{existing ? "Save" : "Create app"}</button>
        </div>
      </form>
    </Dialog>
  );
}

export function KeyDialog({ name, apiKey, onClose }: { name: string; apiKey: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const curl = `curl ${window.location.origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${apiKey}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"fast","messages":[{"role":"user","content":"Say hi"}]}'`;
  return (
    <Dialog title={`Key for ${name}`} onClose={onClose}>
      <p><strong>Copy this key now.</strong> It is shown once and is not stored; only its hash is kept.</p>
      <div className="keybox">
        <code data-testid="api-key">{apiKey}</code>
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard.writeText(apiKey).then(() => { setCopied(true); }).catch(() => { setCopied(false); });
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <p className="muted small">Try it:</p>
      <pre>{curl}</pre>
      <div className="dialog-actions">
        <button type="button" className="primary" onClick={onClose}>I&apos;ve saved it</button>
      </div>
    </Dialog>
  );
}
