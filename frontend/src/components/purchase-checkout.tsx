import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Checkout as BillingCheckout,
  checkoutSessionSchema,
  type CheckoutSource,
  type PayRequest,
  type PayResult,
  type CheckoutSession as PaymentView,
} from "openrails-checkout";
import {
  APIError,
  getSessionGeneration,
  request,
  subscribeSession,
} from "../api";
import { useAuth } from "../auth-context";
import {
  getAttempt,
  rememberCheckout,
  saveAttempt,
  finishCheckout,
  clearAttempt,
} from "../attempts";
import {
  terminalCheckout,
  type Checkout,
  type Offer,
  type Page,
  type PaymentMethod,
  type PaymentOptionsDocument,
  type Post,
} from "../models";
import { Button, ErrorState, Loading } from "./ui";

// One-use gateway tokens stay in memory, never in browser storage or URLs.
const pendingRequests = new Map<string, { generation: number; body: object }>();
let rememberedGeneration = getSessionGeneration();
subscribeSession(() => {
  const next = getSessionGeneration();
  if (next !== rememberedGeneration) {
    pendingRequests.clear();
    rememberedGeneration = next;
  }
});
function statusForView(status: string): PaymentView["status"] {
  return [
    "created",
    "requires_action",
    "processing",
    "succeeded",
    "failed",
    "blocked",
    "expired",
    "canceled",
  ].includes(status)
    ? (status as PaymentView["status"])
    : "processing";
}
function makeSource(
  post: Post,
  offer: Offer,
  buyer: string,
  generation: number,
  document: PaymentOptionsDocument,
  methods: PaymentMethod[],
  changed: (value: Checkout) => void,
) {
  if (
    document.price_id !== offer.price_id ||
    document.product_id !== offer.product_id
  )
    throw new Error(
      "The checkout offer changed. Reload the post before payment.",
    );
  const scope = `post:${buyer}:${post.id}`;
  // Preserve an earlier Stripe attempt created by the previous frontend.
  const legacy = getAttempt(`${scope}:${offer.price_id}`);
  if (
    !getAttempt(scope).submitted &&
    !getAttempt(scope).checkoutID &&
    (legacy.request || legacy.checkoutID)
  ) {
    saveAttempt(scope, {
      ...legacy,
      submitted: true,
      offer: { priceID: offer.price_id, plan: document.plan },
    });
    clearAttempt(`${scope}:${offer.price_id}`);
  }
  const frozenPlan = () => getAttempt(scope).offer?.plan || document.plan;
  const assertBuyer = () => {
    if (getSessionGeneration() !== generation)
      throw new APIError(
        "Your account changed. Reopen checkout with the intended account.",
        409,
        "session_changed",
      );
  };
  let current: Checkout | undefined;
  const rails = document.options.flatMap((option) => {
    const psp = document.psps.find(
      (item) => item.psp_id === option.psp_id && item.key === option.selector,
    );
    if (
      !psp ||
      !["stripe", "nmi"].includes(option.rail) ||
      psp.custodian !== "psp"
    )
      return [];
    if (
      option.rail === "nmi" &&
      (!psp.config?.tokenization_key ||
        !psp.config?.tokenization_url ||
        psp.config.tokenization_key.startsWith("preview_"))
    )
      return [];
    return [
      {
        id: option.psp_id,
        rail: option.rail,
        mode: option.mode as "one_off" | "subscription",
        driver:
          option.rail === "nmi"
            ? ("collect_js" as const)
            : ("redirect" as const),
        public_config: psp.config,
      },
    ];
  });
  const view = (): PaymentView =>
    checkoutSessionSchema.parse({
      id: current?.id || getAttempt(scope).key,
      status: statusForView(
        getAttempt(scope).submitted &&
          (!current || !terminalCheckout(current.status))
          ? "processing"
          : current?.status || "created",
      ),
      merchant: { display_name: post.channel_name || "OnlyDemo" },
      plan: frozenPlan(),
      line_items: [
        {
          label: frozenPlan().display_name,
          amount: frozenPlan().unit_amount,
        },
      ],
      tax: "0",
      due_today: frozenPlan().unit_amount,
      rails,
      saved_methods: methods
        .filter(
          (method) =>
            method.rail === "nmi" &&
            method.health?.active !== false &&
            rails.some((rail) => rail.id === method.psp_id),
        )
        .map((method) => ({
          id: method.id,
          option_id: method.psp_id,
          rail: method.rail,
          brand: method.card?.brand,
          last_four: method.card?.last4,
          exp_month: method.card?.exp_month,
          exp_year: method.card?.exp_year,
        })),
      payment_id: current?.payment_id || undefined,
      subscription_id: current?.subscription_id || undefined,
      expires_at: null,
    });
  const retain = (result: Checkout) => {
    current = result;
    const attempt = getAttempt(scope);
    saveAttempt(scope, { ...attempt, checkoutID: result.id, submitted: true });
    rememberCheckout(result.id, scope);
    try {
      sessionStorage.setItem(`openrails-latest-checkout:${buyer}`, result.id);
    } catch {
      /* The status stays available in this component. */
    }
    changed(result);
  };
  const postOriginal = async () => {
    assertBuyer();
    const attempt = getAttempt(scope);
    const transient = pendingRequests.get(scope);
    const original =
      transient?.generation === generation
        ? transient.body
        : attempt.request?.body;
    if (!original)
      throw new Error(
        "This attempt is unresolved and its one-use card token is no longer in memory. Check your payment history; do not start another attempt.",
      );
    const result = await request<Checkout>(
      `/api/v1/posts/${post.id}/checkout`,
      {
        method: "POST",
        headers: { "Idempotency-Key": attempt.key },
        body: JSON.stringify(original),
      },
    );
    assertBuyer();
    retain(result);
    return result;
  };
  const result = (value: Checkout): PayResult => ({
    status: statusForView(value.status),
    ...(value.url ? { redirect_url: value.url } : {}),
    payment_id: value.payment_id || undefined,
    subscription_id: value.subscription_id || undefined,
  });
  const source: CheckoutSource = {
    async getSession() {
      assertBuyer();
      const id = getAttempt(scope).checkoutID;
      if (id) {
        const loaded = await request<Checkout>(`/api/v1/checkouts/${id}`);
        assertBuyer();
        retain(loaded);
      }
      return view();
    },
    async pay(payment: PayRequest) {
      assertBuyer();
      let attempt = getAttempt(scope);
      if (attempt.checkoutID) {
        const existing = await request<Checkout>(
          `/api/v1/checkouts/${attempt.checkoutID}`,
        );
        assertBuyer();
        retain(existing);
        if (existing.status !== "created" || existing.url)
          return result(existing);
      }
      if (!attempt.submitted) {
        const option = document.options.find(
          (item) => item.psp_id === payment.option_id,
        );
        if (!option)
          throw new Error(
            "This provider is not eligible for the selected offer.",
          );
        const { option_id: _option, ...details } = payment;
        void _option;
        const body = {
          price_id: offer.price_id,
          payment: { ...details, rail: option.selector, psp_id: option.psp_id },
        };
        pendingRequests.set(scope, { generation, body });
        // Saved-method IDs and redirects may resume after reload. A new-card
        // token may only be replayed from this page's in-memory request.
        attempt = {
          ...attempt,
          submitted: true,
          offer: { priceID: offer.price_id, plan: document.plan },
          ...(!payment.payment_token
            ? { request: { path: `/api/v1/posts/${post.id}/checkout`, body } }
            : {}),
        };
        saveAttempt(scope, attempt);
      }
      try {
        return result(await postOriginal());
      } catch (error) {
        // A dispatched request is never a license to re-tokenize. Poll the
        // accepted ID when available; otherwise explicit retry keeps its key.
        current = {
          id: getAttempt(scope).checkoutID || "",
          status: "processing",
          mode: "one_off",
        };
        changed(current);
        if (
          error instanceof APIError &&
          [400, 403, 404, 422].includes(error.status) &&
          !getAttempt(scope).checkoutID
        ) {
          return { status: "blocked", failure_message: error.message };
        }
        return { status: "processing" };
      }
    },
  };
  return { source, retry: postOriginal, scope };
}
export function PurchaseCheckout({
  post,
  offer,
  onComplete,
}: {
  post: Post;
  offer: Offer;
  onComplete: () => void;
}) {
  const { user } = useAuth();
  const client = useQueryClient();
  const generation = getSessionGeneration();
  const [current, setCurrent] = useState<Checkout>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const options = useQuery({
    queryKey: ["checkout-options", offer.price_id, user?.id],
    queryFn: () =>
      request<PaymentOptionsDocument>(
        `/api/v1/checkout/options?price_id=${encodeURIComponent(offer.price_id)}`,
      ),
  });
  const methods = useQuery({
    queryKey: ["payment-methods", user?.id],
    queryFn: () =>
      request<Page<PaymentMethod>>("/billing/v1/me/payment-methods"),
    enabled: !!user,
  });
  const composed = useMemo(() => {
    void revision;
    return options.data && methods.data && user
      ? makeSource(
          post,
          offer,
          user.id,
          generation,
          options.data,
          methods.data.data,
          setCurrent,
        )
      : null;
  }, [options.data, methods.data, user, post, offer, generation, revision]);
  if (options.isPending || methods.isPending) return <Loading />;
  if (options.error || methods.error)
    return <ErrorState error={options.error || methods.error} />;
  if (!composed)
    return <p className="muted">Sign in to choose a payment provider.</p>;
  const retry = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await composed.retry();
      setRevision((value) => value + 1);
      if (result.url && result.status === "requires_action") {
        const url = new URL(result.url);
        if (url.protocol !== "https:" || url.hostname !== "checkout.stripe.com")
          throw new Error("Unexpected checkout destination.");
        location.assign(url.href);
      }
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "The original attempt could not be checked.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="stack">
      <BillingCheckout
        source={composed.source}
        layout="compact"
        appearance={{
          theme: document.documentElement.classList.contains("dark")
            ? "dark"
            : "light",
          variables: {
            primary: getComputedStyle(document.documentElement)
              .getPropertyValue("--primary")
              .trim(),
            primaryForeground: getComputedStyle(document.documentElement)
              .getPropertyValue("--primary-foreground")
              .trim(),
          },
        }}
        onComplete={async (value) => {
          if (value.status === "succeeded") {
            const id = getAttempt(composed.scope).checkoutID;
            if (id) finishCheckout(id);
            pendingRequests.delete(composed.scope);
            await client.invalidateQueries();
            onComplete();
          }
        }}
      />
      {(["processing", "created"].includes(current?.status || "") ||
        (getAttempt(composed.scope).submitted && !current?.id)) && (
        <>
          <p className="fine-print">
            The payment result is unresolved. Any retry below uses the original
            request and key, without collecting another token.
          </p>
          <Button variant="secondary" busy={busy} onClick={() => void retry()}>
            Retry the original request
          </Button>
        </>
      )}
      {current && ["failed", "canceled"].includes(current.status) && (
        <Button
          variant="secondary"
          onClick={() => {
            clearAttempt(composed.scope);
            pendingRequests.delete(composed.scope);
            setCurrent(undefined);
            setError("");
            setRevision((value) => value + 1);
          }}
        >
          Start a new payment attempt
        </Button>
      )}
      {current?.url &&
        current.status === "requires_action" &&
        (() => {
          try {
            const target = new URL(current.url);
            return target.protocol === "https:" &&
              target.hostname === "checkout.stripe.com" ? (
              <a className="text-button" href={target.href}>
                Continue the accepted Stripe checkout
              </a>
            ) : null;
          } catch {
            return null;
          }
        })()}
      {current?.id && !terminalCheckout(current.status) && (
        <a
          className="text-button"
          href={`/checkout/return?checkout_id=${encodeURIComponent(current.id)}`}
        >
          View verified checkout status
        </a>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
