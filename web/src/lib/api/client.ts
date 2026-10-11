import createClient, { type Middleware } from "openapi-fetch";

import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

// Session-scoped request headers, set by SessionProvider after /auth/me or
// login. Kept in module scope so the fetch middleware can read them.
let csrfToken = "";
let workspaceId = "";

export function setSessionHeaders(csrf: string, workspace: string) {
  csrfToken = csrf;
  workspaceId = workspace;
}

/** Headers for hand-rolled fetches (e.g. streaming) that bypass the client. */
export function sessionHeaders(): Record<string, string> {
  const h: Record<string, string> = {};
  if (workspaceId) h["X-Workspace-ID"] = workspaceId;
  if (csrfToken) h["X-CSRF-Token"] = csrfToken;
  return h;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

const sessionHeaderMiddleware: Middleware = {
  onRequest({ request }) {
    if (workspaceId) request.headers.set("X-Workspace-ID", workspaceId);
    if (csrfToken && !SAFE_METHODS.has(request.method)) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
};

// Same-origin requests; next.config.ts rewrites /api/* to the Go API.
export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin" });
api.use(sessionHeaderMiddleware);

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/** Unwraps an openapi-fetch result, throwing ApiError on failure. */
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || !res.response.ok) {
    const e = res.error as { error?: { code?: string; message?: string } } | undefined;
    throw new ApiError(res.response.status, e?.error?.code ?? "error", e?.error?.message ?? res.response.statusText);
  }
  return res.data as T;
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Something went wrong";
}
