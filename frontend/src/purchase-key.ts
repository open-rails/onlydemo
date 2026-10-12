import type { Order } from "@openrails/billing-ui/client";

const storageKey = (viewer: string, price: string) =>
  `onlydemo:purchase:${JSON.stringify([viewer, price])}`;

// Closing checkout or reloading the page must resume the same unpaid order.
// Persist before sending its creation request, including when the response is lost.
export function purchaseKey(viewer: string, price: string): string {
  const name = storageKey(viewer, price);
  const existing = sessionStorage.getItem(name);
  if (existing) return existing;
  const key = crypto.randomUUID();
  sessionStorage.setItem(name, key);
  return key;
}

// An HTTP failure or processing payment says nothing about whether retrying
// needs the same order. Only an authoritative terminal order releases its key.
export function observePurchase(viewer: string, price: string, key: string, status: Order["status"]) {
  if (status !== "complete" && status !== "expired" && status !== "canceled") return;
  const name = storageKey(viewer, price);
  try {
    if (sessionStorage.getItem(name) === key) sessionStorage.removeItem(name);
  } catch {
    // Retaining a terminal order's key is safer than obscuring its outcome.
  }
}
