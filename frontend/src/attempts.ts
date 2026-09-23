import type { CheckoutPlan } from "@openrails/billing-ui";
interface Attempt {
  key: string;
  checkoutID?: string;
  submitted?: boolean;
  offer?: { priceID: string; plan: CheckoutPlan };
  request?: { path: string; body: object };
}
const prefix = "openrails-attempt:";
const fallback = new Map<string, Attempt>();
export function getAttempt(scope: string): Attempt {
  let current = fallback.get(scope);
  try {
    current =
      (JSON.parse(
        sessionStorage.getItem(prefix + scope) || "null",
      ) as Attempt) || current;
  } catch {
    /* Memory keeps retry identity when storage is unavailable. */
  }
  if (!current) current = { key: crypto.randomUUID() };
  saveAttempt(scope, current);
  return current;
}
export function saveAttempt(scope: string, attempt: Attempt) {
  fallback.set(scope, attempt);
  try {
    sessionStorage.setItem(prefix + scope, JSON.stringify(attempt));
  } catch {
    /* Do not invent a second key. */
  }
}
export function clearAttempt(scope: string) {
  fallback.delete(scope);
  try {
    sessionStorage.removeItem(prefix + scope);
  } catch {
    /* In-memory state is cleared. */
  }
}
export function rememberCheckout(checkoutID: string, scope: string) {
  try {
    sessionStorage.setItem("billing-ui:" + checkoutID, scope);
  } catch {
    /* Server status remains authoritative. */
  }
}
export function finishCheckout(checkoutID: string) {
  try {
    const scope = sessionStorage.getItem("billing-ui:" + checkoutID);
    if (scope) clearAttempt(scope);
    sessionStorage.removeItem("billing-ui:" + checkoutID);
  } catch {
    /* Read-only confirmation remains usable. */
  }
}

export function checkoutAttempt(checkoutID: string) {
  try {
    const scope = sessionStorage.getItem("billing-ui:" + checkoutID);
    if (!scope) return null;
    const attempt =
      fallback.get(scope) ||
      (JSON.parse(
        sessionStorage.getItem(prefix + scope) || "null",
      ) as Attempt | null);
    return attempt?.checkoutID === checkoutID ? attempt : null;
  } catch {
    return null;
  }
}
