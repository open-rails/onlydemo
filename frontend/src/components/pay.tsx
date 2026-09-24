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
import {
  CheckoutModal,
  checkoutRails,
  checkoutSessionSchema,
  savedMethodsFor,
  type CheckoutSource,
  type PayRequest,
  type PayResult,
  type CheckoutSession as PanelSession,
} from "@openrails/billing-ui";
import { SignInPanel } from "@openrails/auth-ui";
import { HugeiconsIcon } from "@hugeicons/react";
import { Tick02Icon } from "@hugeicons/core-free-icons";
import { APIError, request, sessionKey } from "../api";
import { AccountBillingScope } from "../billing";
import { clearAttempt, getAttempt, saveAttempt } from "../attempts";
import { useAuth } from "../session";
import type {
  AppConfig,
  Checkout,
  Offer,
  Page,
  PaymentMethod,
  PaymentOptionsDocument,
} from "../models";
import { money } from "../format";
import { ErrorState, Loading } from "./states";

// What the buyer is paying for: a post unlock or a channel membership.
export type PayTarget =
  | { kind: "post"; id: number; merchant: string; offer: Offer }
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
  `Subscribe for ${money(offer.unit_amount, offer.currency)}${perPeriod(offer.access_duration_hours)}`;

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
  const options = useQuery({
    queryKey: ["checkout-options", target.offer.price_id, user?.id],
    queryFn: () =>
      request<PaymentOptionsDocument>(
        `/api/v1/checkout/options?price_id=${encodeURIComponent(target.offer.price_id)}`,
      ),
    enabled: !!user,
  });
  // Stable for the modal's life: saving a card must not restart the panel.
  const source = useMemo(
    () => (user && options.data ? paySource(target, user.id, options.data) : null),
    [target, user, options.data],
  );
  const failed = options.error;
  const gate = !user ? (
    <SignInPanel
      initialTab="login"
      title="Sign in to continue"
      description={`Then pay ${money(target.offer.unit_amount, target.offer.currency)} without leaving this page.`}
      onSignedIn={() => undefined}
    />
  ) : failed ? (
    <ErrorState
      error={failed}
      retry={() => void options.refetch()}
    />
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

const statuses = new Set<PanelSession["status"]>([
  "created",
  "requires_action",
  "processing",
  "succeeded",
  "failed",
  "blocked",
  "expired",
  "canceled",
]);
const ended = (status?: string) =>
  ["succeeded", "failed", "expired", "canceled"].includes(status || "");

// The panel's data source. The host binds buyer, offer and idempotency; the
// panel owns cards, 3-D Secure and retries after a definite decline.
function paySource(
  target: PayTarget,
  buyer: string,
  document: PaymentOptionsDocument,
): CheckoutSource {
  if (document.price_id !== target.offer.price_id)
    throw new Error("The offer changed. Reload the page before paying.");
  const generation = sessionKey();
  const scope = `pay:${target.kind}:${buyer}:${target.id}:${target.offer.price_id}`;
  const rails = checkoutRails(document.options, document.psps);
  const path =
    target.kind === "post"
      ? `/api/v1/posts/${target.id}/checkout`
      : `/api/v1/channels/${target.id}/subscribe`;
  let current: Checkout | undefined;
  let methods: PaymentMethod[] = [];
  let opened = false;
  const guard = () => {
    if (sessionKey() !== generation)
      throw new APIError(
        "Your account changed. Reopen checkout with the intended account.",
        409,
        "session_changed",
      );
  };
  // A membership quote awaits the payer's confirmation; it is not a payment.
  const quoted = (value?: Checkout) =>
    target.kind === "membership" && !!value?.membership_quote && !value.operation;
  const status = (value?: Checkout): PanelSession["status"] => {
    if (!value || quoted(value)) return "created";
    const next = value.status as PanelSession["status"];
    return statuses.has(next) ? next : "processing";
  };
  const view = (): PanelSession =>
    checkoutSessionSchema.parse({
      id: current?.id || getAttempt(scope).key,
      status: status(current),
      merchant: { display_name: target.merchant },
      plan: document.plan,
      line_items: [
        { label: document.plan.display_name, amount: document.plan.unit_amount },
      ],
      tax: "0",
      due_today: document.plan.unit_amount,
      rails,
      saved_methods: savedMethodsFor(methods, rails),
      payment_id: current?.payment_id || undefined,
      subscription_id: current?.subscription_id || undefined,
      operation: current?.operation ?? null,
      failure: current?.failure ?? null,
      expires_at: null,
    });
  const read = async (id: string) => {
    current = await request<Checkout>(`/billing/v1/me/checkout/${encodeURIComponent(id)}`);
    guard();
    // A definite end frees the offer for a new attempt with a new key.
    if (ended(current.status) && current.status !== "succeeded") clearAttempt(scope);
    return current;
  };
  const submit = async (attempt: ReturnType<typeof getAttempt>) => {
    current = await request<Checkout>(attempt.request!.path, {
      method: "POST",
      headers: { "Idempotency-Key": attempt.key },
      body: JSON.stringify(attempt.request!.body),
    });
    guard();
    saveAttempt(scope, { ...attempt, checkoutID: current.id });
    // The customer view carries the membership quote the payer confirms.
    return target.kind === "membership" ? read(current.id) : current;
  };
  const result = (value: Checkout): PayResult => {
    if (value.status === "requires_action" && value.operation)
      return { status: "requires_action", operation_id: value.operation.id };
    if (value.status === "requires_action" && value.url)
      return { status: "requires_action", redirect_url: value.url };
    return {
      status: status(value) === "created" ? "processing" : status(value),
      payment_id: value.payment_id || undefined,
      subscription_id: value.subscription_id || undefined,
      failure: value.failure ?? null,
      failure_message: value.failure?.message,
    };
  };
  // The quote must carry exactly the terms the panel displayed.
  const confirmQuote = async (quote: Checkout) => {
    if (
      quote.amount !== document.plan.unit_amount ||
      quote.currency !== document.plan.currency ||
      quote.membership_quote?.cycle_hours !== document.plan.period_hours
    ) {
      clearAttempt(scope);
      throw new APIError("The membership terms changed. Reopen checkout to review them.", 409);
    }
    current = await request<Checkout>(
      `/billing/v1/me/checkout/${encodeURIComponent(quote.id)}/confirm`,
      {
        method: "POST",
        body: JSON.stringify({ payment: { rail: quote.payment?.rail || quote.rail_data?.rail } }),
      },
    );
    guard();
    return current;
  };
  return {
    async getSession() {
      guard();
      methods = (await request<Page<PaymentMethod>>("/billing/v1/me/payment-methods")).data;
      const attempt = getAttempt(scope);
      if (attempt.checkoutID) {
        await read(attempt.checkoutID);
        // Opening checkout never shows a finished failure; the panel is ready.
        if (!opened && current && ended(current.status) && current.status !== "succeeded")
          current = undefined;
      } else if (attempt.request && opened) {
        // An unanswered request is replayed with its key; never re-sent new.
        await submit(attempt);
      }
      opened = true;
      return view();
    },
    async pay(payment: PayRequest) {
      guard();
      let attempt = getAttempt(scope);
      const method = payment.payment_method_id;
      if (attempt.checkoutID) {
        const existing = await read(attempt.checkoutID);
        if (quoted(existing) && (attempt.request?.body as { payment?: { payment_method_id?: string } })?.payment?.payment_method_id === method)
          return result(await confirmQuote(existing));
        if (!ended(existing.status) && !quoted(existing)) return result(existing);
        clearAttempt(scope);
        attempt = getAttempt(scope);
      }
      const option = document.options.find((item) => item.psp_id === payment.option_id);
      if (!option) throw new Error("This payment option is no longer available.");
      const { option_id: _option, ...details } = payment;
      void _option;
      attempt = {
        ...attempt,
        request: {
          path,
          body: {
            price_id: target.offer.price_id,
            payment: { ...details, rail: option.selector, psp_id: option.psp_id },
          },
        },
      };
      // A one-use card token is never persisted for replay.
      saveAttempt(scope, payment.payment_token ? { key: attempt.key } : attempt);
      try {
        const created = await submit(attempt);
        return result(quoted(created) ? await confirmQuote(created) : created);
      } catch (error) {
        if (error instanceof APIError && error.status === 402) {
          clearAttempt(scope);
          const failure = (error.metadata?.failure ?? null) as PanelSession["failure"];
          return { status: "failed", failure, failure_message: error.message };
        }
        if (error instanceof APIError && [400, 403, 404, 422].includes(error.status)) {
          clearAttempt(scope);
          return { status: "failed", failure_message: error.message };
        }
        // Unknown outcome: keep the key and let the panel check its status.
        return { status: "processing" };
      }
    },
  };
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
