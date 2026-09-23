export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    readonly metadata?: Record<string, unknown>,
  ) {
    super(message);
    this.name = "APIError";
  }
}
export interface Tokens {
  access_token: string;
  refresh_token?: string;
}
export interface User {
  id: string;
  username: string;
  email: string | null;
  has_password: boolean;
}
const storageKey = "openrails-demo-session";
let tokens: Tokens | null = (() => {
  try {
    return JSON.parse(
      sessionStorage.getItem(storageKey) || "null",
    ) as Tokens | null;
  } catch {
    return null;
  }
})();
const listeners = new Set<() => void>();
export function getSession() {
  return tokens;
}
export function subscribeSession(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
export function setSession(next: Tokens | null) {
  tokens = next;
  try {
    if (next) sessionStorage.setItem(storageKey, JSON.stringify(next));
    else sessionStorage.removeItem(storageKey);
  } catch {
    /* The current tab can still use its in-memory session. */
  }
  listeners.forEach((listener) => listener());
}
async function readResponse<T>(response: Response): Promise<T> {
  const body =
    response.status === 204
      ? null
      : ((await response.json().catch(() => null)) as Record<
          string,
          unknown
        > | null);
  if (!response.ok) {
    const envelope = body?.error;
    const error =
      typeof envelope === "object" && envelope !== null
        ? (envelope as {
            message?: string;
            code?: string;
            metadata?: Record<string, unknown>;
          })
        : undefined;
    throw new APIError(
      error?.message ||
        (typeof envelope === "string"
          ? envelope
          : `Request failed (${response.status}). Please try again.`),
      response.status,
      error?.code,
      error?.metadata,
    );
  }
  return body as T;
}
let refreshing: Promise<boolean> | undefined;
async function refreshSession(): Promise<boolean> {
  if (!tokens?.refresh_token) {
    setSession(null);
    return false;
  }
  const oldRefresh = tokens.refresh_token;
  const response = await fetch("/auth/v1/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      grant_type: "refresh_token",
      refresh_token: oldRefresh,
    }),
  });
  if (response.status === 400 || response.status === 401) {
    setSession(null);
    return false;
  }
  const next = await readResponse<Tokens>(response);
  if (!next.access_token)
    throw new APIError(
      "Your session could not be refreshed. Please sign in again.",
      401,
    );
  setSession({ ...next, refresh_token: next.refresh_token || oldRefresh });
  return true;
}
export async function request<T>(
  path: string,
  init: RequestInit = {},
  authenticated = true,
  inspectHeaders?: (headers: Headers) => void,
): Promise<T> {
  const send = () =>
    fetch(path, {
      ...init,
      headers: {
        ...(init.body ? { "Content-Type": "application/json" } : {}),
        ...(authenticated && tokens
          ? { Authorization: `Bearer ${tokens.access_token}` }
          : {}),
        ...init.headers,
      },
    });
  let response = await send();
  if (response.status === 401 && authenticated && tokens) {
    refreshing ??= refreshSession().finally(() => {
      refreshing = undefined;
    });
    if (await refreshing) response = await send();
  }
  if (response.ok) inspectHeaders?.(response.headers);
  return readResponse<T>(response);
}
export const authAPI = {
  me: () => request<User>("/auth/v1/me"),
  login: (identifier: string, password: string) =>
    request<Tokens>(
      "/auth/v1/password/login",
      { method: "POST", body: JSON.stringify({ identifier, password }) },
      false,
    ),
  register: (identifier: string, username: string, password: string) =>
    request<{ token_set: Tokens }>(
      "/auth/v1/register",
      {
        method: "POST",
        body: JSON.stringify({ identifier, username, password }),
      },
      false,
    ),
  logout: () => request<void>("/auth/v1/logout", { method: "DELETE" }),
  recover: (token: string) =>
    request<void>(
      "/auth/v1/account/recovery/confirm",
      { method: "POST", body: JSON.stringify({ token }) },
      false,
    ),
  deleteAccount: (password: string) =>
    request<void>("/auth/v1/user", {
      method: "DELETE",
      body: JSON.stringify({ password }),
    }),
};

export async function postPage(path: string) {
  let next: string | null = null;
  const data = await request<import("./models").Post[]>(
    path,
    {},
    true,
    (headers) => {
      next = headers.get("X-Next-Cursor");
    },
  );
  return { data, next_cursor: next };
}
