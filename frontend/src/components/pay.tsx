import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { OrderCheckout } from "@openrails/billing-ui";
import type { Order } from "@openrails/billing-ui/client";
import { SignInPanel } from "@openrails/auth-ui";
import { sessionIdentity, useSession } from "@openrails/auth-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { Tick02Icon } from "@hugeicons/core-free-icons";
import { request, sessionKey } from "../api";
import { AccountBillingScope } from "../billing";
import { useAuth } from "../session";
import type { Offer } from "../models";
import { money } from "../format";
import { observePurchase, purchaseKey } from "../purchase-key";
import { ErrorState, Loading } from "./states";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";

// What the buyer is paying for: a post unlock or a channel membership.
export type PayTarget =
  // A post by its code; a membership by its channel id.
  | { kind: "post"; id: string; merchant: string; offer: Offer }
  | { kind: "membership"; id: string; merchant: string; offer: Offer };

const PayContext = createContext<(target: PayTarget) => void>(() => {});
/** Opens the one payment modal over the current page. */
export const usePay = () => useContext(PayContext);

export const perPeriod = (hours?: number | null) =>
  !hours
    ? ""
    : hours === 1
      ? "/hour"
      : hours === 24
        ? "/day"
        : hours % 24 === 0
          ? `/${hours / 24} days`
          : `/${hours} hours`;
export const unlockLabel = (offer: Offer) =>
  `Unlock for ${money(offer.unit_amount, offer.currency)}`;
export const subscribeLabel = (offer: Offer) =>
  `Subscribe for ${money(offer.unit_amount, offer.currency)}${perPeriod(offer.billing_interval_hours)}`;

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
      setNotice(done.kind === "post" ? "Unlocked." : `Welcome to ${done.merchant}.`);
      void client.invalidateQueries();
    },
    [client],
  );
  return (
    <PayContext.Provider value={setTarget}>
      {children}
      {target && (
        <AccountBillingScope>
          <PayModal
            key={`${sessionKey()}:${target.kind}:${target.id}:${target.offer.price_id}`}
            target={target}
            onClose={() => setTarget(null)}
            onPaid={() => paid(target)}
          />
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

// The host checks who may buy and creates an order. Billing-ui reads and pays
// that order through the customer API, including the customer's saved cards.
function PayModal({
  target,
  onClose,
  onPaid,
}: {
  target: PayTarget;
  onClose: () => void;
  onPaid: () => void;
}) {
  const session = useSession();
  const viewer = session.status === "authenticated" ? session.userId : null;
  const path =
    target.kind === "post"
      ? `/api/v1/posts/${target.id}/checkout`
      : `/api/v1/channels/${target.id}/subscribe`;
  const order = useQuery({
    queryKey: ["purchase-order", sessionIdentity(session), path, target.offer.price_id],
    queryFn: async () => {
      const idempotencyKey = purchaseKey(viewer!, target.offer.price_id);
      const value = await request<Order>(path, {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ price_id: target.offer.price_id }),
      });
      observePurchase(viewer!, target.offer.price_id, idempotencyKey, value.status);
      return { value, idempotencyKey };
    },
    enabled: viewer !== null,
    // Refreshing content after payment must not issue another create-order POST.
    staleTime: "static",
    gcTime: 0,
    retry: false,
  });
  const gate = session.status === "loading" ? <Loading /> : !viewer ? (
    <SignInPanel
      initialTab="login"
      title="Sign in to continue"
      description={`Then pay ${money(target.offer.unit_amount, target.offer.currency)} without leaving this page.`}
      onSignedIn={() => undefined}
    />
  ) : order.error ? (
    <ErrorState error={order.error} retry={() => void order.refetch()} />
  ) : !order.data ? (
    <Loading />
  ) : undefined;
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{target.kind === "post" ? "Unlock post" : "Join membership"}</DialogTitle>
          <DialogDescription>{target.merchant} · Sandbox payments only</DialogDescription>
        </DialogHeader>
        {gate ?? (order.data && <OrderCheckout
          orderId={order.data.value.id}
          returnUrl={window.location.href}
          onSettled={(paid) => {
            observePurchase(viewer!, target.offer.price_id, order.data.idempotencyKey, paid.status);
            if (paid.status === "complete") onPaid();
          }}
        />)}
      </DialogContent>
    </Dialog>
  );
}

/** Joins a free membership in one click; signed-out buyers sign in first. */
export function useJoinFree(channelID: string) {
  const auth = useAuth();
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const join = async () => {
    if (!auth.user) return auth.openLogin();
    setPending(true);
    setError("");
    try {
      await request(`/api/v1/channels/${channelID}/join`, { method: "POST" });
      await client.invalidateQueries();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not join.");
    } finally {
      setPending(false);
    }
  };
  return { join, pending, error };
}
