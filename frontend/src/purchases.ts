// A purchase changes the viewer's entitlements without a session change.
let purchases = 0;
const listeners = new Set<() => void>();

export const subscribePurchases = (listener: () => void) => {
  listeners.add(listener);
  return () => void listeners.delete(listener);
};
export const purchaseCount = () => purchases;

/** Re-reads ContentKit's access decisions after a purchase settles. */
export function accessChanged() {
  purchases++;
  listeners.forEach((listener) => listener());
}
