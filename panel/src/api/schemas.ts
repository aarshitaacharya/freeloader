// Runtime validation of every admin API response. Each schema is checked against the type
// generated from api/openapi.yaml, so the spec, the generated types and these schemas cannot
// drift apart without `tsc` failing.
import { z } from "zod";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** Fails to compile unless A and B are the same type. */
type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : never) : never;
const same = <A, B>(proof: Same<A, B>): Same<A, B> => proof;

export const appSchema = z.object({
  id: z.number().int(),
  name: z.string(),
  rpm_limit: z.number().int(),
  tokens_per_day: z.number().int(),
  cache_ttl_s: z.number().int(),
  provider_priority: z.array(z.string()),
  cache_all: z.boolean(),
  disabled: z.boolean(),
  created_at: z.string(),
});
same<z.infer<typeof appSchema>, Schemas["App"]>(true);

export const appListSchema = z.object({ apps: z.array(appSchema) });
same<z.infer<typeof appListSchema>, Schemas["AppList"]>(true);

export const createdAppSchema = z.object({ app: appSchema, key: z.string() });
same<z.infer<typeof createdAppSchema>, Schemas["CreatedApp"]>(true);

export const usageRowSchema = z.object({
  day: z.string(),
  app_id: z.number().int(),
  app_name: z.string(),
  provider: z.string(),
  requests: z.number().int(),
  tokens: z.number().int(),
  cache_hits: z.number().int(),
});
same<z.infer<typeof usageRowSchema>, Schemas["UsageRow"]>(true);

export const usageSchema = z.object({ from: z.string(), to: z.string(), rows: z.array(usageRowSchema) });
same<z.infer<typeof usageSchema>, Schemas["Usage"]>(true);

const limitsSchema = z.object({ requests_per_day: z.number().int(), tokens_per_day: z.number().int() });
same<z.infer<typeof limitsSchema>, Schemas["Limits"]>(true);

const modelStatusSchema = z.object({
  model: z.string(),
  requests_today: z.number().int(),
  tokens_today: z.number().int(),
  limits: limitsSchema,
  exhausted: z.boolean(),
});
same<z.infer<typeof modelStatusSchema>, Schemas["ModelStatus"]>(true);

export const providerStatusSchema = z.object({
  name: z.string(),
  state: z.enum(["healthy", "cooling_down", "exhausted", "no_api_key"]),
  cooldown_remaining_s: z.number().int(),
  reset_timezone: z.string(),
  resets_at: z.string(),
  requests_today: z.number().int(),
  tokens_today: z.number().int(),
  limits: limitsSchema,
  models: z.array(modelStatusSchema),
});
same<z.infer<typeof providerStatusSchema>, Schemas["ProviderStatus"]>(true);

export const providerListSchema = z.object({ providers: z.array(providerStatusSchema) });
same<z.infer<typeof providerListSchema>, Schemas["ProviderList"]>(true);

export const errorSchema = z.object({ error: z.object({ code: z.string(), message: z.string() }) });
same<z.infer<typeof errorSchema>, Schemas["Error"]>(true);

export type App = z.infer<typeof appSchema>;
export type NewApp = Schemas["NewApp"];
export type AppPatch = Schemas["AppPatch"];
export type UsageRow = z.infer<typeof usageRowSchema>;
export type ProviderStatus = z.infer<typeof providerStatusSchema>;
export type ModelStatus = z.infer<typeof modelStatusSchema>;
export type ProviderState = ProviderStatus["state"];
