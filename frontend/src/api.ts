import { createAuthClient } from "@openrails/auth-ui/client";
import { createBillingClient } from "@openrails/billing-ui/client";
import { sessionIdentity, sessionUser } from "@openrails/auth-ui/react";

// auth-ui owns the session: cookie restore, the signed-in hint, tab sync and
// AuthKit's contact-proof refusals (ContactProofDialog in auth.tsx).
export const auth = createAuthClient({ baseUrl: "/auth/v1" });
export const billing = createBillingClient({
  baseUrl: "/billing/v1",
  fetch: auth.authFetch,
});
// Changes on sign-in, sign-out, expiry and user switch, not on refresh.
export const sessionKey = () => sessionIdentity(auth.getSnapshot());
export const subscribeSession = auth.subscribe;

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
export async function request<T>(
  path: string,
  init: RequestInit = {},
  authenticated = true,
  inspectHeaders?: (headers: Headers) => void,
): Promise<T> {
  // A request made while the session restores carries the restored session.
  if (authenticated) await auth.ready();
  const signedInAs = () => sessionUser(auth.getSnapshot());
  const before = signedInAs();
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type"))
    headers.set("Content-Type", "application/json");
  const response = authenticated
    ? await auth.authFetch(path, { ...init, headers })
    : await fetch(path, { ...init, headers });
  const body = await readResponse<T>(response);
  // Only a different signed-in user invalidates the answer: signing out, a
  // failed restore or a same-user session rotation does not.
  const after = signedInAs();
  if (authenticated && before && after && before !== after)
    throw new APIError(
      "Your account changed while this request was running. Review its status before trying again.",
      409,
      "session_changed",
    );
  inspectHeaders?.(response.headers);
  return body;
}

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
