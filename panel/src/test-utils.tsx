import { vi } from "vitest";

export interface Call {
  method: string;
  url: string;
  body: unknown;
}

type Handler = (call: Call) => Response | undefined;

export const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** Replaces global fetch with a router; first handler that returns a Response wins. */
export function mockFetch(...handlers: Handler[]): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", (url: string, init?: RequestInit) => {
    const raw = typeof init?.body === "string" ? init.body : undefined;
    const call: Call = { method: init?.method ?? "GET", url, body: raw === undefined ? undefined : (JSON.parse(raw) as unknown) };
    calls.push(call);
    for (const h of handlers) {
      const r = h(call);
      if (r) return Promise.resolve(r);
    }
    return Promise.resolve(json(500, { error: { code: "unhandled", message: `unhandled ${call.method} ${call.url}` } }));
  });
  return calls;
}

export const sampleApp = {
  id: 1, name: "demo", rpm_limit: 60, tokens_per_day: 100000, cache_ttl_s: 86400,
  provider_priority: [] as string[], cache_all: false, disabled: false, created_at: "2026-10-20T00:00:00Z",
};

export const sampleProviders = {
  providers: [
    {
      name: "groq", state: "healthy", cooldown_remaining_s: 0, reset_timezone: "UTC",
      resets_at: new Date(Date.now() + 3 * 3600_000).toISOString(), requests_today: 0, tokens_today: 0,
      limits: { requests_per_day: 0, tokens_per_day: 0 },
      models: [{ model: "gpt-oss-20b", requests_today: 250, tokens_today: 1000, limits: { requests_per_day: 1000, tokens_per_day: 200000 }, exhausted: false }],
    },
    {
      name: "gemini", state: "exhausted", cooldown_remaining_s: 0, reset_timezone: "America/Los_Angeles",
      resets_at: new Date(Date.now() + 5 * 3600_000).toISOString(), requests_today: 475, tokens_today: 0,
      limits: { requests_per_day: 500, tokens_per_day: 0 }, models: [],
    },
    {
      name: "openrouter", state: "no_api_key", cooldown_remaining_s: 0, reset_timezone: "UTC",
      resets_at: new Date(Date.now() + 3600_000).toISOString(), requests_today: 0, tokens_today: 0,
      limits: { requests_per_day: 0, tokens_per_day: 0 }, models: [],
    },
  ],
};
