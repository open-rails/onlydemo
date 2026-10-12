import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { observePurchase, purchaseKey } from "./purchase-key";

describe("unfinished purchase keys", () => {
  beforeEach(() => {
    const values = new Map<string, string>();
    vi.stubGlobal("sessionStorage", {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value); },
      removeItem: (key: string) => { values.delete(key); },
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("resumes the same order after closing, a lost response, and a page reload", async () => {
    const first = purchaseKey("buyer", "price");
    expect(purchaseKey("buyer", "price")).toBe(first);
    // A fresh module has no modal or process memory; only sessionStorage survives.
    vi.resetModules();
    const reloaded = await import("./purchase-key");
    expect(reloaded.purchaseKey("buyer", "price")).toBe(first);
    reloaded.observePurchase("buyer", "price", first, "processing");
    expect(reloaded.purchaseKey("buyer", "price")).toBe(first);
  });

  it("isolates native viewers and prices without losing a returning viewer's order", () => {
    const first = purchaseKey("buyer-a", "price-a");
    expect(purchaseKey("buyer-b", "price-a")).not.toBe(first);
    expect(purchaseKey("buyer-a", "price-b")).not.toBe(first);
    expect(purchaseKey("buyer-a", "price-a")).toBe(first);
  });

  it.each(["complete", "expired", "canceled"] as const)("releases an authoritative %s order, without letting an old response clear a new attempt", (status) => {
    const first = purchaseKey("buyer", "price");
    observePurchase("buyer", "price", first, status);
    const second = purchaseKey("buyer", "price");
    expect(second).not.toBe(first);
    observePurchase("buyer", "price", first, status);
    expect(purchaseKey("buyer", "price")).toBe(second);
  });

  it("fails before order creation if the browser cannot persist its key", () => {
    vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => { throw new Error("Storage unavailable"); } });
    expect(() => purchaseKey("buyer", "price")).toThrow("Storage unavailable");
  });
});
