import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { json, mockFetch, sampleApp, sampleProviders } from "./test-utils";

beforeEach(() => {
  sessionStorage.clear();
  window.location.hash = "";
});
afterEach(() => { vi.unstubAllGlobals(); });

const loggedIn = () => { sessionStorage.setItem("fl-admin-token", "tok"); };
const apps = (...list: object[]) => (c: { method: string; url: string }) =>
  c.url === "/admin/apps" && c.method === "GET" ? json(200, { apps: list }) : undefined;
/** The stat tiles' text, e.g. ["Requests100", ...]; scoped so axis ticks can't collide with it. */
const tiles = (root: HTMLElement) => [...root.querySelectorAll(".tile")].map((t) => t.textContent);
const providers = (c: { url: string }) => (c.url === "/admin/providers" ? json(200, sampleProviders) : undefined);

describe("sign in", () => {
  it("rejects a wrong token and stays signed out", async () => {
    mockFetch(() => json(401, { error: { code: "unauthorized", message: "no" } }));
    render(<App />);
    await userEvent.type(screen.getByLabelText("Admin token"), "wrong");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not accepted");
    expect(sessionStorage.getItem("fl-admin-token")).toBeNull();
  });

  it("explains a gateway with no admin API", async () => {
    mockFetch(() => json(404, { error: { code: "not_found", message: "x" } }));
    render(<App />);
    await userEvent.type(screen.getByLabelText("Admin token"), "t");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("ADMIN_TOKEN");
  });

  it("accepts the right token, remembers it for the session, and signs out", async () => {
    const calls = mockFetch(apps(sampleApp), providers);
    render(<App />);
    await userEvent.type(screen.getByLabelText("Admin token"), "tok");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Apps" })).toBeInTheDocument();
    expect(sessionStorage.getItem("fl-admin-token")).toBe("tok");
    expect(calls.length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(screen.getByLabelText("Admin token")).toBeInTheDocument();
    expect(sessionStorage.getItem("fl-admin-token")).toBeNull();
  });
});

describe("Apps page", () => {
  it("lists apps and shows 0 limits as unlimited", async () => {
    loggedIn();
    mockFetch(apps({ ...sampleApp, rpm_limit: 0, tokens_per_day: 0, cache_ttl_s: 0, provider_priority: ["groq", "gemini"], disabled: true }), providers);
    render(<App />);
    const row = await screen.findByRole("row", { name: /demo/ });
    expect(within(row).getAllByText("unlimited")).toHaveLength(2);
    expect(within(row).getByText("off")).toBeInTheDocument();
    expect(within(row).getByText("groq → gemini")).toBeInTheDocument();
    expect(within(row).getByText("Disabled")).toBeInTheDocument();
  });

  it("creates an app, shows the key once, and never again", async () => {
    loggedIn();
    let list: object[] = [];
    const calls = mockFetch(
      (c) => (c.method === "POST" ? ((list = [sampleApp]), json(201, { app: sampleApp, key: "fl_secretkey123" })) : undefined),
      (c) => apps(...list)(c),
      providers,
    );
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "New app" }));
    const dialog = screen.getByRole("dialog", { name: "New app" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "demo");
    await userEvent.click(within(dialog).getByRole("button", { name: "groq" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Create app" }));

    const keyDialog = await screen.findByRole("dialog", { name: "Key for demo" });
    expect(within(keyDialog).getByTestId("api-key")).toHaveTextContent("fl_secretkey123");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "demo", rpm_limit: 60, tokens_per_day: 100000, cache_ttl_s: 86400, provider_priority: ["groq"], cache_all: false,
    });

    await userEvent.click(within(keyDialog).getByRole("button", { name: /saved it/ }));
    expect(screen.queryByText(/fl_secretkey123/)).not.toBeInTheDocument();
    expect(await screen.findByRole("row", { name: /demo/ })).toBeInTheDocument();
  });

  it("refuses bad limits without calling the API", async () => {
    loggedIn();
    const calls = mockFetch(apps(), providers);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "New app" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Name"), "x");
    const rpm = within(dialog).getByLabelText("Requests per minute");
    await userEvent.clear(rpm);
    await userEvent.type(rpm, "-3");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create app" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("whole numbers");
    await userEvent.clear(rpm);
    await userEvent.click(within(dialog).getByRole("button", { name: "Create app" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("whole numbers"); // empty is not silently 0
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("requires a name", async () => {
    loggedIn();
    const calls = mockFetch(apps(), providers);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "New app" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Create app" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("name");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("keeps the dialog open and shows the server's message when the name is taken", async () => {
    loggedIn();
    mockFetch(
      (c) => (c.method === "POST" ? json(409, { error: { code: "name_taken", message: "an app with that name already exists" } }) : undefined),
      apps(sampleApp), providers,
    );
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "New app" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Name"), "demo");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create app" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("already exists");
    expect(within(dialog).getByRole("button", { name: "Create app" })).toBeEnabled(); // can fix and retry
  });

  it("edits limits with a PATCH that contains no name", async () => {
    loggedIn();
    const calls = mockFetch((c) => (c.method === "PATCH" ? json(200, { ...sampleApp, rpm_limit: 5 }) : undefined), apps(sampleApp), providers);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit demo" }));
    const dialog = screen.getByRole("dialog", { name: "Edit demo" });
    expect(within(dialog).queryByLabelText("Name")).not.toBeInTheDocument();
    const rpm = within(dialog).getByLabelText("Requests per minute");
    await userEvent.clear(rpm);
    await userEvent.type(rpm, "5");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => { expect(calls.some((c) => c.method === "PATCH")).toBe(true); });
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.url).toBe("/admin/apps/1");
    expect(patch?.body).toMatchObject({ rpm_limit: 5 });
    expect(patch?.body).not.toHaveProperty("name");
  });

  it("disables an app", async () => {
    loggedIn();
    const calls = mockFetch((c) => (c.method === "PATCH" ? json(200, { ...sampleApp, disabled: true }) : undefined), apps(sampleApp), providers);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Disable demo" }));
    await waitFor(() => { expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ disabled: true }); });
  });

  it("signs out when the token stops working mid-session", async () => {
    loggedIn();
    mockFetch((c) => (c.method === "PATCH" ? json(401, { error: { code: "unauthorized", message: "bad token" } }) : undefined), apps(sampleApp), providers);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Disable demo" }));
    expect(await screen.findByLabelText("Admin token")).toBeInTheDocument();
  });
});

describe("Providers page", () => {
  it("shows state as icon + label, limits and burn", async () => {
    loggedIn();
    window.location.hash = "#/providers";
    mockFetch(providers, apps());
    render(<App />);
    const groq = await screen.findByRole("article", { name: "groq" });
    expect(within(groq).getByText("Healthy")).toBeInTheDocument();
    const meter = within(groq).getByRole("meter", { name: /Requests: 250 of 1000/ });
    expect(meter).toHaveAttribute("aria-valuenow", "250");
    expect(within(groq).getByText(/250 \/ 1K requests \(25%\)/)).toBeInTheDocument();

    const gemini = screen.getByRole("article", { name: "gemini" });
    expect(within(gemini).getByText("Exhausted")).toBeInTheDocument();
    expect(within(gemini).getByRole("meter").className).toContain("critical"); // 95% of 500

    const or = screen.getByRole("article", { name: "openrouter" });
    expect(within(or).getByText("No API key")).toBeInTheDocument();
    expect(within(or).getByText(/No daily limits configured/)).toBeInTheDocument();
  });

  it("shows an error instead of zeros when the API fails", async () => {
    loggedIn();
    window.location.hash = "#/providers";
    mockFetch(() => json(503, { error: { code: "cache_unavailable", message: "cannot read quota counters from fl-cache" } }));
    render(<App />);
    expect(await screen.findByRole("alert")).toHaveTextContent("fl-cache");
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
  });
});

describe("Usage page", () => {
  const today = new Date().toISOString().slice(0, 10);
  const rows = [
    { day: today, app_id: 1, app_name: "web", provider: "groq", requests: 30, tokens: 300, cache_hits: 3 },
    { day: today, app_id: 1, app_name: "web", provider: "gemini", requests: 10, tokens: 100, cache_hits: 0 },
    { day: today, app_id: 2, app_name: "batch", provider: "groq", requests: 60, tokens: 600, cache_hits: 30 },
  ];
  const usage = (c: { url: string }) => (c.url.startsWith("/admin/usage") ? json(200, { from: today, to: today, rows }) : undefined);

  it("totals, legend, tooltip and table view", async () => {
    loggedIn();
    window.location.hash = "#/usage";
    mockFetch(usage, apps());
    const { container } = render(<App />);
    await waitFor(() => { expect(tiles(container)).toEqual(["Requests100", "Tokens1K", "Cache hit rate33%"]); }); // 30+10+60 requests, 33 cache hits
    const legend = screen.getByRole("list", { name: "Legend" });
    expect(within(legend).getAllByRole("listitem").map((l) => l.textContent)).toEqual(["groq", "gemini"]);

    await userEvent.hover(await screen.findByTestId(`hit-${today}`));
    expect(screen.getByRole("status")).toHaveTextContent("groq: 90");

    await userEvent.click(screen.getByRole("button", { name: "Show table" }));
    const table = screen.getByRole("table");
    expect(within(table).getByRole("columnheader", { name: "groq" })).toBeInTheDocument();
    expect(within(table).getByRole("row", { name: new RegExp(today) })).toHaveTextContent("90");
  });

  it("filtering to one app recomputes totals but keeps each provider's colour", async () => {
    loggedIn();
    window.location.hash = "#/usage";
    mockFetch(usage, apps());
    const { container } = render(<App />);
    await waitFor(() => { expect(tiles(container)[0]).toBe("Requests100"); });
    const colorOf = (label: string) =>
      [...container.querySelectorAll(".legend li")].find((li) => li.textContent === label)?.querySelector<HTMLElement>(".swatch")?.style.background;
    const before = colorOf("groq");
    await userEvent.selectOptions(screen.getByLabelText("App"), "web");
    await waitFor(() => { expect(tiles(container)).toEqual(["Requests40", "Tokens400", "Cache hit rate7.5%"]); });
    expect(colorOf("groq")).toBe(before);
    expect(before).toBeTruthy();
  });

  it("with more than 8 apps the chart folds the smallest into Other, but the table names every app", async () => {
    loggedIn();
    window.location.hash = "#/usage";
    const many = Array.from({ length: 10 }, (_, i) => ({
      day: today, app_id: i + 1, app_name: `app-${String(i + 1).padStart(2, "0")}`, provider: "groq", requests: 100 - i * 5, tokens: 1, cache_hits: 0,
    }));
    mockFetch((c) => (c.url.startsWith("/admin/usage") ? json(200, { from: today, to: today, rows: many }) : undefined), apps());
    render(<App />);
    await screen.findByRole("list", { name: "Legend" });
    await userEvent.selectOptions(screen.getByLabelText("Group by"), "app");
    const legend = screen.getByRole("list", { name: "Legend" });
    const labels = within(legend).getAllByRole("listitem").map((l) => l.textContent);
    expect(labels).toHaveLength(9); // 8 colours + Other
    expect(labels.at(-1)).toBe("Other");
    expect(labels).not.toContain("app-10");

    await userEvent.click(screen.getByRole("button", { name: "Show table" }));
    const headers = within(screen.getByRole("table")).getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toContain("app-10");
    expect(headers).not.toContain("Other");
  });

  it("says so when there is no traffic", async () => {
    loggedIn();
    window.location.hash = "#/usage";
    mockFetch((c) => (c.url.startsWith("/admin/usage") ? json(200, { from: today, to: today, rows: [] }) : undefined), apps());
    render(<App />);
    expect(await screen.findByText("No traffic in this range.")).toBeInTheDocument();
  });
});
