import { useState } from "react";
import {
  SavePaymentMethod,
  authenticatePayment,
  canAuthenticatePayment,
  canSavePaymentMethod,
  type PspConfig,
} from "@openrails/billing-ui";
import { useBillingClient } from "@openrails/billing-ui/react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { sessionKey, request } from "../api";
import { AccountBillingScope } from "../billing";
import { useAuth } from "../session";
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
} from "../models";
import { money, duration } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Add01Icon,
  Alert02Icon,
  ArrowRight02Icon,
  Book02Icon,
  Tick02Icon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState, FormError, Loading } from "./states";

const setupReturnURL = (id: string) =>
  `${location.origin}/me?tab=billing&setup_id=${encodeURIComponent(id)}`;

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
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value) onClose();
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Join {channel.name}</DialogTitle>
          <DialogDescription>
            Membership includes current and future membership posts. Separately
            priced posts remain separate purchases.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <AccountBillingScope>
            <MembershipFlow
              key={`${auth.user?.id}:${sessionKey()}:${channel.id}`}
              channel={channel}
              onClose={onClose}
            />
          </AccountBillingScope>
        )}
      </DialogContent>
    </Dialog>
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
  // A membership renews on a saved card, so only PSPs that can save one here.
  const available = (options.data?.options || []).filter((option) =>
    options.data?.psps.some(
      (psp) => psp.psp_id === option.psp_id && canSavePaymentMethod(psp),
    ),
  );
  const selected =
    available.find((option) => option.psp_id === providerID) || available[0];
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
      <div className="flex flex-col gap-4">
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
        psps={config.data?.psps || []}
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
      <Alert>
        <HugeiconsIcon icon={Alert02Icon} />
        <AlertDescription>
          No eligible payment provider is currently available for this
          membership.
        </AlertDescription>
      </Alert>
    );
  const current = offers.find((offer) => offer.price_id === price);
  const saved =
    methods.data?.data.filter(
      (card) =>
        card.psp_id === selected.psp_id && card.health?.active !== false,
    ) || [];
  return (
    <div className="flex flex-col gap-4">
      {current ? (
        <>
          <OfferSummary offer={current} />
          {offers.length > 1 && (
            <Field>
              <FieldLabel htmlFor="membership-offer">Choose an offer</FieldLabel>
              <NativeSelect
                id="membership-offer"
                value={price}
                onChange={(e) => setPrice(e.target.value)}
              >
                {offers.map((offer) => (
                  <NativeSelectOption value={offer.price_id} key={offer.price_id}>
                    {money(offer.unit_amount, offer.currency)} ·{" "}
                    {duration(offer.access_duration_hours)}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
          )}
          <Field>
            <FieldLabel htmlFor="membership-provider">Payment provider</FieldLabel>
            <NativeSelect
              id="membership-provider"
              value={selected.psp_id}
              onChange={(event) => {
                setProviderID(event.target.value);
                setMethod("");
                setAddCard(false);
              }}
            >
              {available.map((option) => (
                <NativeSelectOption key={option.psp_id} value={option.psp_id}>
                  {options.data?.psps.find(
                    (psp) => psp.psp_id === option.psp_id,
                  )?.display_name || option.selector}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </Field>
          {saved.length > 0 && (
            <Field>
              <FieldLabel htmlFor="membership-card">Saved card</FieldLabel>
              <NativeSelect
                id="membership-card"
                value={method}
                onChange={(e) => setMethod(e.target.value)}
              >
                <NativeSelectOption value="">Choose a card</NativeSelectOption>
                {saved.map((card) => (
                  <NativeSelectOption key={card.id} value={card.id}>
                    {card.card?.brand || "Card"} ending{" "}
                    {card.card?.last4 || "••••"}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
          )}
          {addCard || saved.length === 0 ? (
            <SavePaymentMethod
              key={`${sessionKey()}:${provider.psp_id}`}
              psp={provider}
              returnURL={setupReturnURL}
              appearance={{ theme: "inherit" }}
              onSaved={(id) => {
                setMethod(id);
                setAddCard(false);
                void methods.refetch();
              }}
            />
          ) : (
            <Button variant="outline" onClick={() => setAddCard(true)}>
              <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
              Add another card
            </Button>
          )}
          {(addCard || saved.length === 0) && (
            <Button
              variant="outline"
              disabled={methods.isFetching}
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
              {methods.isFetching && <Spinner data-icon="inline-start" />}
              Check saved cards
            </Button>
          )}
          {create.error && <ErrorState error={create.error} />}
          <Button
            disabled={!method || addCard || !selected || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending && <Spinner data-icon="inline-start" />}
            Review membership
            <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
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
    <Alert>
      <HugeiconsIcon icon={Book02Icon} />
      <AlertTitle>{offer.product_name}</AlertTitle>
      <AlertDescription>
        {money(offer.unit_amount, offer.currency)} ·{" "}
        {duration(offer.access_duration_hours)}
      </AlertDescription>
    </Alert>
  );
}
export function MembershipConfirmation({
  checkout,
  psps,
  onDone,
}: {
  checkout: Checkout;
  psps: PspConfig[];
  onDone: (status: string) => void;
}) {
  const client = useQueryClient();
  const billing = useBillingClient();
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
  const psp = psps.find(
    (item) =>
      item.key === (current.payment?.rail || current.rail_data?.rail),
  );
  const authenticate = async () => {
    if (!current.operation || !psp) return;
    setAuthenticating(true);
    setError("");
    try {
      if (
        (await authenticatePayment(billing, current.operation.id, psp)) ===
        "not_required"
      )
        throw new Error(
          "No additional card authentication is currently available. Check status again.",
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
      <div className="flex flex-col gap-4">
        <Alert>
          <HugeiconsIcon icon={Tick02Icon} className="text-success" />
          <AlertDescription>
            Your membership is confirmed. You can now read the channel’s
            included posts.
          </AlertDescription>
        </Alert>
        <Button onClick={() => onDone(current.status)}>Start reading</Button>
      </div>
    );
  if (terminalCheckout(current.status))
    return (
      <div className="flex flex-col gap-4">
        <Alert>
          <HugeiconsIcon icon={Alert02Icon} />
          <AlertDescription>
            This membership attempt is {current.status}. No access is inferred
            from a payment redirect.
          </AlertDescription>
        </Alert>
        <Button
          variant="outline"
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
    <div className="flex flex-col gap-4">
      <Badge>
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
          variant="outline"
          disabled={prepare.isPending}
          onClick={() => prepare.mutate()}
        >
          {prepare.isPending && <Spinner data-icon="inline-start" />}
          Restore the original membership quote
        </Button>
      )}
      <FormError>{prepare.error?.message}</FormError>
      {current.operation ? (
        <>
          <Alert>
            <Spinner />
            <AlertDescription>
              Waiting for the verified payment result. You may safely return to
              My library; do not start another payment.
            </AlertDescription>
          </Alert>
          {!terminalCheckout(current.status) &&
            psp &&
            canAuthenticatePayment(psp) && (
              <Button
                disabled={authenticating}
                onClick={() => {
                  void authenticate();
                }}
              >
                {authenticating && <Spinner data-icon="inline-start" />}
                Check card authentication
              </Button>
            )}
          <Button variant="outline" onClick={() => void state.refetch()}>
            Check status
          </Button>
          <Button
            variant="link"
            nativeButton={false}
            render={<Link to="/me?tab=billing" />}
          >
            View my memberships
          </Button>
        </>
      ) : (
        <>
          <Field orientation="horizontal">
            <Checkbox
              id="membership-accept"
              checked={accepted}
              onCheckedChange={(value) => setAccepted(value === true)}
            />
            <FieldLabel htmlFor="membership-accept" className="font-normal">
              I agree to pay{" "}
              {current.amount && current.currency
                ? money(current.amount, current.currency)
                : "the quoted amount"}{" "}
              {duration(quote?.cycle_hours)} until I cancel.
            </FieldLabel>
          </Field>
          <Button
            disabled={
              confirm.isPending ||
              !accepted ||
              !current.amount ||
              !quote ||
              !psp
            }
            onClick={() => confirm.mutate()}
          >
            {confirm.isPending && <Spinner data-icon="inline-start" />}
            Confirm and pay
          </Button>
        </>
      )}
      <FormError>{confirm.error?.message || state.error?.message || error}</FormError>
    </div>
  );
}
function finishAttempt(id: string) {
  finishCheckout(id);
}
