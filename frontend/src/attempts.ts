// One payment attempt per buyer and offer. Its idempotency key survives
// reloads until the attempt ends; a definite decline starts a new key.
export interface Attempt {
  key: string;
  checkoutID?: string;
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
