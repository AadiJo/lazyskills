import type { DiscoveredSkill, Preview, PreviewRequest, RegistrySkill, ScanPayload, Scope, UpdatePlan } from "./types";

export class APIError extends Error {
  constructor(message: string, public status: number) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...init.headers } : init?.headers
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new APIError(payload.error || `${response.status} ${response.statusText}`, response.status);
  return payload as T;
}

export const api = {
  scan: () => request<ScanPayload>("/api/scan"),
  content: (scope: Scope, name: string) => request<{ html: string }>(`/api/skills/content?scope=${encodeURIComponent(scope)}&name=${encodeURIComponent(name)}`),
  preview: (input: PreviewRequest) => request<Preview>("/api/actions/preview", { method: "POST", body: JSON.stringify(input) }),
  execute: (preview: Preview, key: string) => request<{ job_id: string; existing: boolean; events_url: string }>("/api/actions/execute", {
    method: "POST",
    headers: { "Idempotency-Key": key },
    body: JSON.stringify({ preview_hash: preview.hash, generation: preview.generation, idempotency_key: key })
  }),
  registry: (query: string) => request<{ query: string; results: RegistrySkill[] }>(`/api/registry/search?q=${encodeURIComponent(query)}`),
  source: (sourceID: string) => request<{ source_id: string; label: string; skills: DiscoveredSkill[] }>(`/api/sources/${encodeURIComponent(sourceID)}/skills`),
  update: () => request<UpdatePlan>("/api/update")
};
