import { lazy, Suspense, useState, useMemo, type FormEvent } from "react";
import {
  Elements,
  PaymentElement,
  useElements,
  useStripe,
} from "@stripe/react-stripe-js";
import type { TokenizedCardData } from "openrails-checkout";
import { loadStripe } from "@stripe/stripe-js";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { getSessionGeneration, request } from "../api";
import { useAuth } from "../auth-context";
import {
  clearAttempt,
  checkoutAttempt,
  finishCheckout,
  getAttempt,
  rememberCheckout,
  saveAttempt,
} from "../attempts";
import {
  terminalCheckout,
  type AppConfig,
  type Channel,
  type Checkout,
  type Offer,
  type Page,
  type PaymentMethod,
  type PaymentOptionsDocument,
  type PublicProvider,
} from "../models";
import { money, duration } from "../format";
import { Badge, Button, ErrorState, Field, Icon, Loading, Modal } from "./ui";

const TokenizedCardForm = lazy(() =>
  import("openrails-checkout").then((module) => ({
    default: module.TokenizedCardForm,
  })),
);

interface Setup {
  id: string;
  status: string;
  client_secret?: string;
  payment_method_id?: string;
}
export function MembershipDialog({
  channel,
  open,
  onClose,
}: {
  channel: Channel;
  open: boolean;
  onClose: () => void;
}) {
  const auth = useAuth();
  return (
    <Modal
      open={open}
      onOpenChange={(value) => {
        if (!value) onClose();
      }}
      title={`Join ${channel.name}`}
      description="Membership includes current and future membership posts. Separately priced posts remain separate purchases."
    >
      {open && <MembershipFlow key={`${auth.user?.id}:${getSessionGeneration()}:${channel.id}`} channel={channel} onClose={onClose} />}
    </Modal>
  );
}
function MembershipFlow({
  channel,
  onClose,
}: {
  channel: Channel;
  onClose: () => void;
}) {
  const auth = useAuth();
  const client = useQueryClient();
  const offers = channel.offers.filter((offer) => offer.auto_renew);
  const [price, setPrice] = useState(offers[0]?.price_id || "");
  const [method, setMethod] = useState("");
  const [providerID, setProviderID] = useState("");
  const [addCard, setAddCard] = useState(false);
  const [quote, setQuote] = useState<Checkout | null>(null);
  const currentScope = `membership-current:${auth.user?.id}:${channel.id}`;
  const [resumeID] = useState(() => getAttempt(currentScope).checkoutID);
  const resume = useQuery({
    queryKey: ["resume-membership", resumeID, auth.user?.id],
    queryFn: () => request<Checkout>(`/billing/v1/me/checkout/${resumeID}`),
    enabled: !!resumeID && !!auth.user,
  });
  const config = useQuery({
    queryKey: ["config"],
    queryFn: () => request<AppConfig>("/api/v1/config", {}, false),
  });
  const options = useQuery({
    queryKey: ["membership-options", price, auth.user?.id],
    queryFn: () =>
      request<PaymentOptionsDocument>(
        `/api/v1/checkout/options?price_id=${encodeURIComponent(price)}`,
      ),
    enabled: !!price,
  });
  const available = (options.data?.options || []).filter(
    (option) =>
      ["stripe", "nmi"].includes(option.rail) &&
      options.data?.psps.some(
        (psp) => psp.psp_id === option.psp_id && psp.custodian === "psp",
      ),
  );
  const selected =
    available.find((option) => option.psp_id === providerID) ||
    available.find((option) => option.rail === "stripe") ||
    available[0];
  const provider = options.data?.psps.find(
    (psp) => psp.psp_id === selected?.psp_id,
  );
  const methods = useQuery({
    queryKey: ["payment-methods", auth.user?.id],
    queryFn: () =>
      request<Page<PaymentMethod>>("/billing/v1/me/payment-methods"),
    enabled: !!auth.user,
  });
  const create = useMutation({
    mutationFn: async () => {
      const scope = `membership:${auth.user?.id}:${channel.id}:${price}:${method}`;
      const attempt = getAttempt(scope);
      if (attempt.checkoutID)
        return request<Checkout>(
          `/billing/v1/me/checkout/${attempt.checkoutID}`,
        );
      const original = attempt.request || {
        path: `/api/v1/channels/${channel.id}/subscribe`,
        body: {
          price_id: price,
          payment: {
            rail: selected?.selector,
            psp_id: selected?.psp_id,
            payment_method_id: method,
          },
        },
      };
      saveAttempt(scope, { ...attempt, request: original });
      const result = await request<Checkout>(original.path, {
        method: "POST",
        headers: { "Idempotency-Key": attempt.key },
        body: JSON.stringify(original.body),
      });
      saveAttempt(scope, {
        ...attempt,
        request: original,
        checkoutID: result.id,
      });
      rememberCheckout(result.id, scope);
      saveAttempt(currentScope, {
        ...getAttempt(currentScope),
        checkoutID: result.id,
      });
      return request<Checkout>(`/billing/v1/me/checkout/${result.id}`);
    },
    onSuccess: setQuote,
  });
  if (!auth.user)
    return (
      <div className="stack">
        <p>Sign in before saving a card or joining this channel.</p>
        <Button
          onClick={() => {
            onClose();
            auth.openLogin();
          }}
        >
          Sign in
        </Button>
      </div>
    );
  if (config.isPending || methods.isPending || (options.isPending && !!price))
    return <Loading />;
  if (config.error || methods.error || options.error)
    return (
      <ErrorState
        error={config.error || methods.error || options.error}
        retry={() => {
          void config.refetch();
          void methods.refetch();
          void options.refetch();
        }}
      />
    );
  if (resumeID && resume.isPending) return <Loading />;
  if (resume.error)
    return (
      <ErrorState error={resume.error} retry={() => void resume.refetch()} />
    );
  const existing = quote || resume.data;
  if (existing)
    return (
      <MembershipConfirmation
        checkout={existing}
        publishableKey={config.data?.stripe_publishable_key || null}
        onDone={async (status) => {
          if (status !== "expired") {
            clearAttempt(currentScope);
            finishCheckout(existing.id);
          }
          await client.invalidateQueries();
          onClose();
        }}
      />
    );
  if (!selected || !provider)
    return (
      <div className="notice notice-warning">
        <Icon name="warning" />
        <p>
          No eligible payment provider is currently available for this
          membership.
        </p>
      </div>
    );
  if (
    selected.rail === "stripe" &&
    !config.data?.stripe_publishable_key?.startsWith("pk_test_")
  )
    return (
      <div className="notice notice-warning">
        <p>Stripe test card entry is not configured.</p>
      </div>
    );
  const current = offers.find((offer) => offer.price_id === price);
  const saved =
    methods.data?.data.filter(
      (card) =>
        card.rail === selected.rail &&
        card.psp_id === selected.psp_id &&
        card.health?.active !== false,
    ) || [];
  return (
    <div className="stack">
      {current ? (
        <>
          <OfferSummary offer={current} />
          {offers.length > 1 && (
            <Field label="Choose an offer">
              <select value={price} onChange={(e) => setPrice(e.target.value)}>
                {offers.map((offer) => (
                  <option value={offer.price_id} key={offer.price_id}>
                    {money(offer.unit_amount, offer.currency)} ·{" "}
                    {duration(offer.access_duration_hours)}
                  </option>
                ))}
              </select>
            </Field>
          )}
          <Field label="Payment provider">
            <select
              value={selected.psp_id}
              onChange={(event) => {
                setProviderID(event.target.value);
                setMethod("");
                setAddCard(false);
              }}
            >
              {available.map((option) => (
                <option key={option.psp_id} value={option.psp_id}>
                  {options.data?.psps.find(
                    (psp) => psp.psp_id === option.psp_id,
                  )?.display_name || option.selector}{" "}
                  · {option.rail === "nmi" ? "NMI sandbox" : "Stripe test"}
                </option>
              ))}
            </select>
          </Field>
          {saved.length > 0 && (
            <Field label="Saved card">
              <select
                value={method}
                onChange={(e) => setMethod(e.target.value)}
              >
                <option value="">Choose a card</option>
                {saved.map((card) => (
                  <option key={card.id} value={card.id}>
                    {card.card?.brand || "Card"} ending{" "}
                    {card.card?.last4 || "••••"}
                  </option>
                ))}
              </select>
            </Field>
          )}
          {addCard || saved.length === 0 ? (
            selected.rail === "nmi" ? (
              <NMICardSetup
                key={`${getSessionGeneration()}:${provider.psp_id}`}
                provider={provider}
                onSaved={(id) => {
                  setMethod(id);
                  setAddCard(false);
                  void methods.refetch();
                }}
              />
            ) : (
              <CardSetup
                config={{ ...config.data!, stripe_psp_id: selected.psp_id }}
                onSaved={(id) => {
                  setMethod(id);
                  setAddCard(false);
                  void methods.refetch();
                }}
              />
            )
          ) : (
            <Button variant="secondary" onClick={() => setAddCard(true)}>
              <Icon name="plus" size={15} />
              Add another card
            </Button>
          )}
          {(addCard || saved.length === 0) && (
            <Button
              variant="secondary"
              busy={methods.isFetching}
              onClick={async () => {
                const checked = await methods.refetch();
                if (
                  checked.data?.data.some(
                    (card) =>
                      card.psp_id === selected.psp_id &&
                      card.health?.active !== false,
                  )
                )
                  setAddCard(false);
              }}
            >
              Check saved cards
            </Button>
          )}
          {create.error && <ErrorState error={create.error} />}
          <Button
            disabled={!method || addCard || !selected}
            busy={create.isPending}
            onClick={() => create.mutate()}
          >
            Review membership
            <Icon name="arrow" size={16} />
          </Button>
          <p className="fine-print">
            Review the exact price and renewal period before agreeing to
            payment. Saving a card alone does not start a subscription.
          </p>
        </>
      ) : (
        <p className="muted">
          This channel does not currently offer a membership.
        </p>
      )}
    </div>
  );
}
function OfferSummary({ offer }: { offer: Offer }) {
  return (
    <div className="notice">
      <Icon name="book" />
      <div>
        <strong>{offer.product_name}</strong>
        <p>
          {money(offer.unit_amount, offer.currency)} ·{" "}
          {duration(offer.access_duration_hours)}
        </p>
      </div>
    </div>
  );
}
function CardSetup({
  config,
  onSaved,
}: {
  config: AppConfig;
  onSaved: (id: string) => void;
}) {
  const [consent, setConsent] = useState(false);
  const [setup, setSetup] = useState<Setup | null>(null);
  const { user } = useAuth();
  const create = useMutation({
    mutationFn: () =>
      request<Setup>("/billing/v1/me/payment-methods/stripe-setup", {
        method: "POST",
        headers: {
          "Idempotency-Key": getAttempt(`card-setup:${user?.id}`).key,
        },
        body: JSON.stringify({ psp_id: config.stripe_psp_id, consent: true }),
      }),
    onSuccess: (value) => {
      if (value.payment_method_id) {
        clearAttempt(`card-setup:${user?.id}`);
        onSaved(value.payment_method_id);
      } else setSetup(value);
    },
  });
  const stripe = useMemo(
    () =>
      config.stripe_publishable_key?.startsWith("pk_test_")
        ? loadStripe(config.stripe_publishable_key)
        : null,
    [config.stripe_publishable_key],
  );
  const dark = document.documentElement.classList.contains("dark");
  if (setup?.client_secret && stripe)
    return (
      <Elements
        stripe={stripe}
        options={{
          clientSecret: setup.client_secret,
          appearance: {
            theme: dark ? "night" : "stripe",
            variables: {
              colorPrimary: dark ? "#f05a6a" : "#c92135",
              borderRadius: "7px",
            },
          },
        }}
      >
        <SaveCard
          setup={setup}
          onSaved={(id) => {
            clearAttempt(`card-setup:${user?.id}`);
            onSaved(id);
          }}
        />
      </Elements>
    );
  return (
    <div className="stack">
      <label className="checkbox-field">
        <input
          type="checkbox"
          checked={consent}
          onChange={(e) => setConsent(e.target.checked)}
        />
        <span>
          I allow this card to be saved for future payments that I separately
          agree to.
        </span>
      </label>
      <Button
        variant="secondary"
        disabled={!consent}
        busy={create.isPending}
        onClick={() => create.mutate()}
      >
        Enter card details securely
      </Button>
      {create.error && (
        <p className="form-error" role="alert">
          {create.error.message}
        </p>
      )}
    </div>
  );
}
function SaveCard({
  setup,
  onSaved,
}: {
  setup: Setup;
  onSaved: (id: string) => void;
}) {
  const stripe = useStripe();
  const elements = useElements();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!stripe || !elements) return;
    setBusy(true);
    setError("");
    try {
      const validation = await elements.submit();
      if (validation.error) throw new Error(validation.error.message);
      const result = await stripe.confirmSetup({
        elements,
        confirmParams: {
          return_url: `${location.origin}/me?tab=billing&setup_id=${encodeURIComponent(setup.id)}`,
        },
        redirect: "if_required",
      });
      if (result.error) throw new Error(result.error.message);
      const verified = await request<Setup>(
        `/billing/v1/me/payment-methods/stripe-setup/${setup.id}/confirm`,
        { method: "POST" },
      );
      if (!verified.payment_method_id)
        throw new Error(
          "Card verification is pending. Open Billing in My library to check it before trying again.",
        );
      onSaved(verified.payment_method_id);
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Card setup could not be confirmed.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="stack"
      onSubmit={(event) => {
        void submit(event);
      }}
    >
      <PaymentElement />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <Button
        type="submit"
        variant="secondary"
        disabled={!stripe || !elements}
        busy={busy}
      >
        Save test card
      </Button>
    </form>
  );
}
export function MembershipConfirmation({
  checkout,
  publishableKey,
  onDone,
}: {
  checkout: Checkout;
  publishableKey: string | null;
  onDone: (status: string) => void;
}) {
  const client = useQueryClient();
  const [accepted, setAccepted] = useState(false);
  const [error, setError] = useState("");
  const [authenticating, setAuthenticating] = useState(false);
  const state = useQuery({
    queryKey: ["membership-checkout", checkout.id],
    queryFn: () => request<Checkout>(`/billing/v1/me/checkout/${checkout.id}`),
    initialData: checkout,
    refetchInterval: (query) =>
      query.state.data?.operation && !terminalCheckout(query.state.data.status)
        ? 1600
        : false,
  });
  const confirm = useMutation({
    mutationFn: () =>
      request<Checkout>(`/billing/v1/me/checkout/${checkout.id}/confirm`, {
        method: "POST",
        body: JSON.stringify({
          payment: {
            rail: state.data.payment?.rail || state.data.rail_data?.rail,
          },
        }),
      }),
    onSuccess: (value) => {
      client.setQueryData(["membership-checkout", checkout.id], value);
      void state.refetch();
      if (["succeeded", "failed", "canceled"].includes(value.status))
        finishAttempt(value.id);
    },
  });
  const prepare = useMutation({
    mutationFn: async () => {
      const previous = checkoutAttempt(checkout.id);
      if (!previous?.request)
        throw new Error(
          "The original quote request is unavailable in this browser.",
        );
      return request<Checkout>(previous.request.path, {
        method: "POST",
        headers: { "Idempotency-Key": previous.key },
        body: JSON.stringify(previous.request.body),
      });
    },
    onSuccess: async () => {
      await state.refetch();
    },
  });
  const current = state.data;
  const quote = current.membership_quote || checkout.membership_quote;
  const authenticate = async () => {
    if (
      !current.operation ||
      current.payment?.rail !== "stripe" ||
      !publishableKey?.startsWith("pk_test_")
    )
      return;
    setAuthenticating(true);
    setError("");
    try {
      const details = await request<{ client_secret?: string }>(
        `/billing/v1/me/payment-operations/${current.operation.id}/authentication`,
      );
      if (!details.client_secret)
        throw new Error(
          "No additional card authentication is currently available. Check status again.",
        );
      const stripe = await loadStripe(publishableKey);
      if (!stripe) throw new Error("Could not load Stripe.");
      const result = await stripe.confirmCardPayment(details.client_secret);
      if (result.error) throw new Error(result.error.message);
      await request(
        `/billing/v1/me/payment-operations/${current.operation.id}/authentication/confirm`,
        { method: "POST" },
      );
      await state.refetch();
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Authentication could not be completed.",
      );
    } finally {
      setAuthenticating(false);
    }
  };
  if (current.status === "succeeded")
    return (
      <div className="stack">
        <div className="notice notice-success">
          <Icon name="check" />
          <p>
            Your membership is confirmed. You can now read the channel’s
            included posts.
          </p>
        </div>
        <Button onClick={() => onDone(current.status)}>Start reading</Button>
      </div>
    );
  if (terminalCheckout(current.status))
    return (
      <div className="stack">
        <div className="notice notice-warning">
          <Icon name="warning" />
          <p>
            This membership attempt is {current.status}. No access is inferred
            from a payment redirect.
          </p>
        </div>
        <Button
          variant="secondary"
          onClick={() => {
            if (current.status !== "expired") finishAttempt(current.id);
            onDone(current.status);
          }}
        >
          Back to channel
        </Button>
      </div>
    );
  return (
    <div className="stack">
      <Badge tone="brand">
        {current.operation
          ? "Payment being confirmed"
          : "Review your membership"}
      </Badge>
      <h3>{quote?.product_name || "Channel membership"}</h3>
      <div className="purchase-price">
        {current.amount && current.currency
          ? money(current.amount, current.currency)
          : "Price unavailable"}{" "}
        <small>{duration(quote?.cycle_hours)}</small>
      </div>
      <p className="muted">
        Includes membership posts while your subscription is active. Separately
        priced posts are extra. Purchased posts remain yours after cancellation.
      </p>
      {!quote && !current.operation && (
        <Button
          variant="secondary"
          busy={prepare.isPending}
          onClick={() => prepare.mutate()}
        >
          Restore the original membership quote
        </Button>
      )}
      {prepare.error && (
        <p className="form-error" role="alert">
          {prepare.error.message}
        </p>
      )}
      {current.operation ? (
        <>
          <div className="notice">
            <span className="spinner" />
            <p>
              Waiting for the verified payment result. You may safely return to
              My library; do not start another payment.
            </p>
          </div>
          {!terminalCheckout(current.status) &&
            current.payment?.rail === "stripe" && (
              <Button
                busy={authenticating}
                onClick={() => {
                  void authenticate();
                }}
              >
                Check card authentication
              </Button>
            )}
          <Button variant="secondary" onClick={() => void state.refetch()}>
            Check status
          </Button>
          <Link to="/me?tab=subscriptions" className="text-button">
            View my memberships
          </Link>
        </>
      ) : (
        <>
          <label className="checkbox-field">
            <input
              type="checkbox"
              checked={accepted}
              onChange={(e) => setAccepted(e.target.checked)}
            />
            <span>
              I agree to pay{" "}
              {current.amount && current.currency
                ? money(current.amount, current.currency)
                : "the quoted amount"}{" "}
              {duration(quote?.cycle_hours)} until I cancel.
            </span>
          </label>
          <Button
            disabled={
              !accepted ||
              !current.amount ||
              !quote ||
              !["stripe", "nmi"].includes(
                current.payment?.rail || current.rail_data?.rail || "",
              )
            }
            busy={confirm.isPending}
            onClick={() => confirm.mutate()}
          >
            Confirm and pay
          </Button>
        </>
      )}
      {(confirm.error || state.error || error) && (
        <p className="form-error" role="alert">
          {confirm.error?.message || state.error?.message || error}
        </p>
      )}
    </div>
  );
}
function finishAttempt(id: string) {
  finishCheckout(id);
}

function NMICardSetup({
  provider,
  onSaved,
}: {
  provider: PublicProvider;
  onSaved: (id: string) => void;
}) {
  const [consent, setConsent] = useState(false);
  const [error, setError] = useState("");
  const generation = getSessionGeneration();
  const key = provider.config?.tokenization_key;
  const url = provider.config?.tokenization_url;
  if (!key || !url || key.startsWith("preview_"))
    return (
      <p className="notice notice-warning">
        This provider’s secure card entry is unavailable.
      </p>
    );
  const save = async (card: TokenizedCardData) => {
    if (getSessionGeneration() !== generation)
      throw new Error("Your account changed. Reopen card setup.");
    setError("");
    try {
      const method = await request<PaymentMethod>(
        "/billing/v1/me/payment-methods",
        {
          method: "POST",
          body: JSON.stringify({ ...card, provider: provider.key }),
        },
      );
      if (!method.id)
        throw new Error(
          "The server did not confirm a saved card. Check your saved payment methods.",
        );
      onSaved(method.id);
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Card setup could not be confirmed.",
      );
      throw cause;
    }
  };
  return (
    <div className="stack">
      <label className="checkbox-field">
        <input
          type="checkbox"
          checked={consent}
          onChange={(event) => setConsent(event.target.checked)}
        />
        <span>
          I allow this card to be saved for future payments that I separately
          agree to.
        </span>
      </label>
      <Suspense fallback={<Loading />}>
        <TokenizedCardForm
          key={`${generation}:${provider.psp_id}`}
          tokenizationKey={key}
          tokenizationURL={url}
          disabled={!consent}
          onTokenized={save}
          submitLabel="Save sandbox card"
          appearance={{
            theme: document.documentElement.classList.contains("dark")
              ? "dark"
              : "light",
          }}
        />
      </Suspense>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
