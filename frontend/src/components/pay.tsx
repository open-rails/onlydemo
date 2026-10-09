import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckoutModal, type CheckoutSource } from "@openrails/billing-ui";
import type { CheckoutSessionLink } from "@openrails/billing-ui/client";
import { SignInPanel } from "@openrails/auth-ui";
import { HugeiconsIcon } from "@hugeicons/react";
import { Tick02Icon } from "@hugeicons/core-free-icons";
import { billing, request, sessionKey } from "../api";
import { AccountBillingScope } from "../billing";
import { useAuth } from "../session";
import type { AppConfig, Offer } from "../models";
import { money } from "../format";
import { ErrorState, Loading } from "./states";

// What the buyer is paying for: a post unlock or a channel membership.
export type PayTarget =
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

// The app mints the buyer's checkout session (its rules decide who may buy);
// billing-ui reads and pays it on the customer surface, saved cards included.
function PayModal({
  target,
  onClose,
  onPaid,
}: {
  target: PayTarget;
  onClose: () => void;
  onPaid: () => void;
}) {
  const { user } = useAuth();
  const config = useQuery({
    queryKey: ["config"],
    queryFn: () => request<AppConfig>("/api/v1/config", {}, false),
  });
  const path =
    target.kind === "post"
      ? `/api/v1/posts/${target.id}/checkout`
      : `/api/v1/channels/${target.id}/subscribe`;
  const session = useQuery({
    queryKey: ["checkout-session", sessionKey(), path, target.offer.price_id],
    queryFn: () =>
      request<CheckoutSessionLink>(path, {
        method: "POST",
        body: JSON.stringify({ price_id: target.offer.price_id }),
      }),
    enabled: !!user,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });
  // Stable for the modal's life: saving a card must not restart the panel.
  const source = useMemo(
    () =>
      session.data
        ? billing.checkoutSource(session.data.id, { customerBase: "/billing/v1/me" })
        : null,
    [session.data],
  );
  const gate = !user ? (
    <SignInPanel
      initialTab="login"
      title="Sign in to continue"
      description={`Then pay ${money(target.offer.unit_amount, target.offer.currency)} without leaving this page.`}
      onSignedIn={() => undefined}
    />
  ) : session.error ? (
    <ErrorState error={session.error} retry={() => void session.refetch()} />
  ) : !source ? (
    <Loading />
  ) : undefined;
  return (
    <CheckoutModal
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      gate={gate}
      source={source ?? idleSource}
      layout="compact"
      appearance={{ theme: "inherit" }}
      defaultCountry={config.data?.country || undefined}
      onComplete={(result) => {
        if (result.status === "succeeded") onPaid();
      }}
    />
  );
}

const idleSource: CheckoutSource = {
  getSession: () => new Promise(() => undefined),
  pay: () => Promise.resolve({ status: "processing" }),
};

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
