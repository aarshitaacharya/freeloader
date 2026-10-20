import { AdminClient, ApiError, ContractError } from "./client";

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const app = {
  id: 1, name: "demo", rpm_limit: 60, tokens_per_day: 100000, cache_ttl_s: 86400,
  provider_priority: [], cache_all: false, disabled: false, created_at: "2026-10-20T00:00:00Z",
};

function client(handler: (url: string, init: RequestInit) => Response) {
  const calls: { url: string; init: RequestInit }[] = [];
  const f = ((url: string, init: RequestInit) => {
    calls.push({ url, init });
    return Promise.resolve(handler(url, init));
  }) as typeof fetch;
  return { c: new AdminClient("tok", f), calls };
}

describe("AdminClient", () => {
  it("sends the bearer token and parses a valid list", async () => {
    const { c, calls } = client(() => json(200, { apps: [app] }));
    expect(await c.listApps()).toEqual([app]);
    expect(new Headers(calls[0]?.init.headers).get("Authorization")).toBe("Bearer tok");
  });

  it("sends JSON bodies for create and update", async () => {
    const { c, calls } = client((_, init) => (init.method === "POST" ? json(201, { app, key: "fl_abc" }) : json(200, app)));
    expect(await c.createApp({ name: "demo" })).toEqual({ app, key: "fl_abc" });
    await c.updateApp(3, { disabled: true });
    expect(calls[0]).toMatchObject({ url: "/admin/apps", init: { method: "POST", body: '{"name":"demo"}' } });
    expect(calls[1]).toMatchObject({ url: "/admin/apps/3", init: { method: "PATCH", body: '{"disabled":true}' } });
  });

  it("turns an error payload into an ApiError carrying status and code", async () => {
    const { c } = client(() => json(409, { error: { code: "name_taken", message: "an app with that name already exists" } }));
    const err = await c.createApp({ name: "x" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 409, code: "name_taken", message: "an app with that name already exists" });
  });

  it("flags 401 as unauthorized", async () => {
    const { c } = client(() => json(401, { error: { code: "unauthorized", message: "no" } }));
    expect(await c.listApps().catch((e: unknown) => e)).toMatchObject({ unauthorized: true });
  });

  it("still gives an ApiError when an error body is not our JSON (e.g. a proxy's HTML page)", async () => {
    const { c } = client(() => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }));
    expect(await c.listApps().catch((e: unknown) => e)).toMatchObject({ name: "ApiError", status: 502 });
  });

  it("rejects a 2xx body that breaks the contract instead of passing it on", async () => {
    const broken = { ...app } as Record<string, unknown>;
    delete broken["cache_all"];
    const { c } = client(() => json(200, { apps: [broken] }));
    await expect(c.listApps()).rejects.toBeInstanceOf(ContractError);
  });

  it("rejects an unknown provider state", async () => {
    const p = {
      name: "g", state: "on_fire", cooldown_remaining_s: 0, reset_timezone: "UTC", resets_at: "2026-10-21T00:00:00Z",
      requests_today: 0, tokens_today: 0, limits: { requests_per_day: 0, tokens_per_day: 0 }, models: [],
    };
    const { c } = client(() => json(200, { providers: [p] }));
    await expect(c.listProviders()).rejects.toBeInstanceOf(ContractError);
  });

  it("asks for usage with from and to", async () => {
    const { c, calls } = client(() => json(200, { from: "2026-10-14", to: "2026-10-20", rows: [] }));
    await c.getUsage("2026-10-14", "2026-10-20");
    expect(calls[0]?.url).toBe("/admin/usage?from=2026-10-14&to=2026-10-20");
  });
});
