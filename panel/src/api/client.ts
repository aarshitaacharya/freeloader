import type { z } from "zod";
import {
  appListSchema,
  appSchema,
  createdAppSchema,
  errorSchema,
  providerListSchema,
  usageSchema,
  type App,
  type AppPatch,
  type NewApp,
} from "./schemas";

/** The server answered with an error status. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
  get unauthorized(): boolean {
    return this.status === 401;
  }
}

/** The server answered 2xx but the body does not match the OpenAPI contract. */
export class ContractError extends Error {
  constructor(path: string, detail: string) {
    super(`unexpected response from ${path}: ${detail}`);
    this.name = "ContractError";
  }
}

type Fetch = typeof fetch;

export class AdminClient {
  constructor(
    private readonly token: string,
    private readonly fetchImpl: Fetch = (...a) => fetch(...a),
  ) {}

  private async request<S extends z.ZodType>(
    method: "GET" | "POST" | "PATCH",
    path: string,
    schema: S,
    body?: unknown,
  ): Promise<z.infer<S>> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}` };
    const init: RequestInit = { method, headers };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const res = await this.fetchImpl(path, init);
    const text = await res.text();
    let json: unknown;
    try {
      json = text === "" ? null : JSON.parse(text);
    } catch {
      throw res.ok
        ? new ContractError(path, "body is not JSON")
        : new ApiError(res.status, "http_error", `${String(res.status)} ${res.statusText}`);
    }
    if (!res.ok) {
      const err = errorSchema.safeParse(json);
      throw err.success
        ? new ApiError(res.status, err.data.error.code, err.data.error.message)
        : new ApiError(res.status, "http_error", `${String(res.status)} ${res.statusText}`);
    }
    const parsed = schema.safeParse(json);
    if (!parsed.success) throw new ContractError(path, parsed.error.message);
    return parsed.data;
  }

  async listApps(): Promise<App[]> {
    return (await this.request("GET", "/admin/apps", appListSchema)).apps;
  }
  createApp(app: NewApp): Promise<{ app: App; key: string }> {
    return this.request("POST", "/admin/apps", createdAppSchema, app);
  }
  updateApp(id: number, patch: AppPatch): Promise<App> {
    return this.request("PATCH", `/admin/apps/${String(id)}`, appSchema, patch);
  }
  getUsage(from: string, to: string) {
    const q = new URLSearchParams({ from, to });
    return this.request("GET", `/admin/usage?${q.toString()}`, usageSchema);
  }
  async listProviders() {
    return (await this.request("GET", "/admin/providers", providerListSchema)).providers;
  }
}
