import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { OrderPayment } from "@openrails/billing-ui";
import type { Order, Product } from "@openrails/billing-ui/client";
import { SignInPanel } from "@openrails/auth-ui";
import { sessionIdentity, useSession } from "@openrails/auth-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { Tick02Icon } from "@hugeicons/core-free-icons";
import { request, sessionKey } from "../api";
import { AccountBillingScope } from "../billing";
import { accessChanged } from "../purchases";
import { observePurchase, purchaseKey } from "../purchase-key";
import { Button } from "./ui/button";
import { ErrorState, Loading } from "./states";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";

// ContentKit supplies the access key; the modal resolves its current
// product and price through OpenRails.
export interface PayTarget {
  key: string;
  merchant: string;
}

const PayContext = createContext<(target: PayTarget) => void>(() => {});
/** Opens the one payment modal over the current page. */
export const usePay = () => useContext(PayContext);

/** A ContentKit onCheckout that pays in the modal; merchant names the seller. */
export function useCheckout(merchant: string) {
  const pay = usePay();
  return useCallback(
    ({ key }: { key: string }) => {
      pay({ key, merchant });
    },
    [pay, merchant],
  );
}

export function PayHost({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  const [target, setTarget] = useState<PayTarget | null>(null);
  const [notice, setNotice] = useState("");
  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 4000);
    return () => window.clearTimeout(timer);
  }, [notice]);
  const paid = useCallback(
    (done: PayTarget) => {
      setTarget(null);
      setNotice(done.key.startsWith("onlydemo:group:") ? `Welcome to ${done.merchant}.` : "Unlocked.");
      accessChanged();
      void client.invalidateQueries();
    },
    [client],
  );
  return (
    <PayContext.Provider value={setTarget}>
      {children}
      {target && (
        <AccountBillingScope>
          <PayModal key={`${sessionKey()}:${target.key}`} target={target} onClose={() => setTarget(null)} onPaid={() => paid(target)} />
        </AccountBillingScope>
      )}
      {notice && (
        <div className="pay-toast" role="status">
          <HugeiconsIcon icon={Tick02Icon} size={18} />
          {notice}
        </div>
      )}
    </PayContext.Provider>
  );
}

// The buyer's order for the sale's OpenRails price; billing-ui reads and
// pays it through the customer API, including the customer's saved cards.
function PayModal({ target, onClose, onPaid }: { target: PayTarget; onClose: () => void; onPaid: () => void }) {
  const session = useSession();
  const viewer = session.status === "authenticated" ? session.userId : null;
  const product = useQuery({queryKey: ["product", target.key], queryFn: () => request<Product | null>(`/api/v1/products?key=${encodeURIComponent(target.key)}`)});
  const price = product.data?.prices[0]?.id ?? "";
  const free = product.data && !product.data.archived && product.data.prices.length === 0 && product.data.description === "OnlyDemo free membership" && target.key.startsWith("onlydemo:group:");
  const [joining, setJoining] = useState(false);
  const [joinError, setJoinError] = useState<unknown>();
  const order = useQuery({
    queryKey: ["purchase-order", sessionIdentity(session), price],
    queryFn: async () => {
      if (!price) throw new Error("This price is not on sale right now.");
      const idempotencyKey = purchaseKey(viewer!, price);
      const value = await request<Order>("/billing/v1/me/orders", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ lines: [{ price_id: price }] }),
      });
      observePurchase(viewer!, price, idempotencyKey, value.status);
      return { value, idempotencyKey };
    },
    enabled: viewer !== null && !!price,
    // Refreshing content after payment must not issue another create-order POST.
    staleTime: "static",
    gcTime: 0,
    retry: false,
  });
  const membership = target.key.startsWith("onlydemo:group:");
  const gate =
    session.status === "loading" ? (
      <Loading />
    ) : !viewer ? (
      <SignInPanel initialTab="login" title="Sign in to continue" description="Then pay without leaving this page." onSignedIn={() => undefined} />
    ) : product.isPending ? (
      <Loading />
    ) : product.error ? (
      <ErrorState error={product.error} retry={() => void product.refetch()} />
    ) : free ? (
      <><Button disabled={joining} onClick={() => {setJoining(true); void request(`/api/v1/memberships/${target.key.split(":")[2]}`, {method:"POST"}).then(onPaid).catch(setJoinError).finally(() => setJoining(false));}}>Join free</Button>{joinError ? <ErrorState error={joinError} /> : null}</>
    ) : !price || product.data?.archived ? (
      <p>This access is not currently for sale.</p>
    ) : order.error ? (
      <ErrorState error={order.error} retry={() => void order.refetch()} />
    ) : !order.data ? (
      <Loading />
    ) : undefined;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{membership ? "Join membership" : "Unlock post"}</DialogTitle>
          <DialogDescription>{target.merchant} · Sandbox payments only</DialogDescription>
        </DialogHeader>
        {gate ??
          (order.data && (
            <OrderPayment
              orderId={order.data.value.id}
              returnUrl={window.location.href}
              onSettled={(paid) => {
                observePurchase(viewer!, price, order.data.idempotencyKey, paid.status);
                if (paid.status === "complete") onPaid();
              }}
            />
          ))}
      </DialogContent>
    </Dialog>
  );
}
