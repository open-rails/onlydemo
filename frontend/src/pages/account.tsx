import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { authAPI, request, setSession } from "../api";
import type {
  AccountData,
  Page,
  PaymentMethod,
  Payment,
  Subscription,
} from "../models";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Field,
  Icon,
  Loading,
  Modal,
} from "../components/ui";
import { PostCard, ChannelCard } from "../components/cards";
import { useAuth } from "../auth-context";
import { date, duration, money } from "../format";

const titles: Record<string, string> = {
  library: "Purchased",
  subscriptions: "Subscriptions",
  channels: "My channels",
  billing: "Payments",
  settings: "Account",
};
export function AccountPage() {
  const [search, setSearch] = useSearchParams();
  const tab = search.get("tab") || "library";
  const auth = useAuth();
  const summary = useInfiniteQuery({
    queryKey: ["me-dashboard", auth.user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      request<AccountData & { has_more?: boolean; next_cursor?: string }>(
        `/api/v1/me${pageParam ? `?before=${encodeURIComponent(pageParam)}` : ""}`,
      ),
    getNextPageParam: (last) =>
      last.has_more && last.next_cursor ? last.next_cursor : undefined,
  });
  const current = summary.data?.pages[0];
  return (
    <>
      <div className="page-title">
        <h1>{titles[tab] || "Account"}</h1>
        <span className="muted">@{auth.user?.username}</span>
      </div>
      <div className="tabs" aria-label="Account sections">
        {[
          ["library", "Purchased"],
          ["subscriptions", "Subscriptions"],
          ["channels", "My channels"],
          ["billing", "Payments"],
          ["settings", "Account"],
        ].map(([key, label]) => (
          <button
            key={key}
            className={`tab ${tab === key ? "active" : ""}`}
            onClick={() => setSearch({ tab: key })}
          >
            {label}
          </button>
        ))}
      </div>
      {search.get("setup_id") && <SetupReturn id={search.get("setup_id")!} />}
      {summary.error && (
        <ErrorState
          error={summary.error}
          retry={() => void summary.refetch()}
        />
      )}
      {summary.isPending ? (
        <Loading />
      ) : summary.error ? null : (
        <>
          {tab === "library" && (
            <>
              <Library
                posts={
                  summary.data?.pages.flatMap((page) => page.purchased_posts) ||
                  []
                }
                searching={!!summary.hasNextPage}
              />
              {summary.hasNextPage && (
                <div className="pagination">
                  <Button
                    variant="secondary"
                    busy={summary.isFetchingNextPage}
                    onClick={() => void summary.fetchNextPage()}
                  >
                    Look for older purchases
                  </Button>
                </div>
              )}
            </>
          )}
          {tab === "subscriptions" && (
            <Subscriptions initial={current?.subscriptions} />
          )}
          {tab === "channels" && (
            <>
              <div className="section-heading">
                <div>
                  <h2>Channels you run</h2>
                  <p>
                    Only channels where you can manage or edit publication
                    appear here.
                  </p>
                </div>
                <Link className="button button-primary" to="/channels/new">
                  <Icon name="plus" size={16} />
                  Create channel
                </Link>
              </div>
              {current?.manageable_channels?.length ? (
                <div className="channel-grid">
                  {current.manageable_channels.map((channel) => (
                    <ChannelCard channel={channel} key={channel.id} />
                  ))}
                </div>
              ) : (
                <EmptyState icon="settings" title="No editorial channels yet.">
                  Create a channel or accept an editor invitation to start
                  publishing.
                </EmptyState>
              )}
            </>
          )}
          {tab === "billing" && <Billing />}
          {tab === "settings" && <AccountSettings />}
        </>
      )}
    </>
  );
}
function Library({
  posts,
  searching,
}: {
  posts: AccountData["purchased_posts"];
  searching: boolean;
}) {
  const unique = [...new Map(posts.map((post) => [post.id, post])).values()];
  return unique.length ? (
    <div className="feed">
      {unique.map((post) => (
        <PostCard post={{ ...post, purchased: true }} key={post.id} />
      ))}
    </div>
  ) : (
    <EmptyState
      icon="book"
      title={
        searching ? "No purchases on this page." : "Nothing unlocked yet."
      }
      action={
        !searching ? (
          <Link to="/channels" className="button button-primary">
            Explore creators
          </Link>
        ) : undefined
      }
    >
      {searching
        ? "Search older posts below to find earlier purchases."
        : "Purchased posts appear here while their content is available. Payment history is retained separately."}
    </EmptyState>
  );
}
function Subscriptions({ initial }: { initial?: Subscription[] }) {
  const auth = useAuth();
  const client = useQueryClient();
  const [cancel, setCancel] = useState<Subscription | null>(null);
  const subscriptions = useQuery({
    queryKey: ["subscriptions", auth.user?.id],
    queryFn: () =>
      request<Page<Subscription>>("/billing/v1/me/subscriptions?limit=100"),
    initialData: initial ? { data: initial } : undefined,
  });
  const change = useMutation({
    mutationFn: ({
      id,
      action,
      feedback,
    }: {
      id: string;
      action: "cancel" | "resume";
      feedback?: string;
    }) =>
      request(`/billing/v1/me/subscriptions/${id}/${action}`, {
        method: "POST",
        body: JSON.stringify(feedback ? { feedback } : {}),
      }),
    onSuccess: async () => {
      setCancel(null);
      await client.invalidateQueries();
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (cancel)
      change.mutate({
        id: cancel.id,
        action: "cancel",
        feedback: String(
          new FormData(event.currentTarget).get("feedback"),
        ).trim(),
      });
  };
  if (subscriptions.isPending) return <Loading />;
  if (subscriptions.error)
    return (
      <ErrorState
        error={subscriptions.error}
        retry={() => void subscriptions.refetch()}
      />
    );
  return (
    <>
      {subscriptions.data?.data.length ? (
        <div className="panel">
          <div className="panel-heading">
            <div>
              <h2>Channel memberships</h2>
              <p>
                Cancellation follows the server’s agreement and access end date.
                Purchased posts remain yours.
              </p>
            </div>
          </div>
          <div className="data-list">
            {subscriptions.data.data.map((subscription) => (
              <div className="data-row" key={subscription.id}>
                <span className="avatar">
                  <Icon name="users" size={16} />
                </span>
                <div className="data-row-main">
                  <h3>
                    {subscription.channel_name ||
                      subscription.product?.display_name ||
                      "Channel membership"}
                  </h3>
                  <p>
                    {subscription.cancel_scheduled
                      ? "Access through"
                      : "Current period ends"}{" "}
                    {date(subscription.current_period_ends_at)}
                  </p>
                  {subscription.price && (
                    <p>
                      {money(
                        subscription.price.unit_amount,
                        subscription.price.currency,
                      )}{" "}
                      · {duration(subscription.price.access_duration_hours)}
                    </p>
                  )}
                </div>
                <Badge
                  tone={
                    subscription.cancel_scheduled
                      ? "warning"
                      : subscription.status === "active"
                        ? "success"
                        : "neutral"
                  }
                >
                  {subscription.cancel_scheduled
                    ? "Cancellation scheduled"
                    : subscription.status}
                </Badge>
                <div className="data-row-actions">
                  {subscription.cancel_portal_url ? (
                    <a
                      className="button button-secondary"
                      target="_blank"
                      rel="noopener noreferrer"
                      href={subscription.cancel_portal_url}
                    >
                      Manage cancellation
                      <Icon name="external" size={14} />
                    </a>
                  ) : (
                    !subscription.cancel_scheduled &&
                    ["active", "pending", "past_due"].includes(
                      subscription.status,
                    ) && (
                      <Button
                        variant="secondary"
                        onClick={() => setCancel(subscription)}
                      >
                        Cancel membership
                      </Button>
                    )
                  )}
                  {subscription.resumable && (
                    <Button
                      variant="secondary"
                      busy={change.isPending}
                      onClick={() =>
                        change.mutate({ id: subscription.id, action: "resume" })
                      }
                    >
                      Resume
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
          {change.error && !cancel && (
            <p className="form-error" role="alert">
              {change.error.message}
            </p>
          )}
        </div>
      ) : (
        <EmptyState
          icon="users"
          title="No subscriptions yet."
          action={
            <Link to="/channels" className="button button-secondary">
              Explore creators
            </Link>
          }
        >
          Join a channel to read its included posts while subscribed.
        </EmptyState>
      )}
      <Modal
        open={!!cancel}
        onOpenChange={(value) => {
          if (!value) setCancel(null);
        }}
        title="Cancel this membership?"
        description="Your purchased posts remain yours. The server will confirm when included membership access ends."
      >
        <form className="stack" onSubmit={submit}>
          <Field label="Reason for cancelling">
            <textarea
              name="feedback"
              minLength={4}
              maxLength={500}
              required
              placeholder="Tell the creator why you’re leaving."
            />
          </Field>
          {change.error && (
            <p className="form-error" role="alert">
              {change.error.message}
            </p>
          )}
          <div className="form-actions">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setCancel(null)}
            >
              Keep membership
            </Button>
            <Button variant="danger" type="submit" busy={change.isPending}>
              Confirm cancellation
            </Button>
          </div>
        </form>
      </Modal>
    </>
  );
}
function Billing() {
  const { user } = useAuth();
  const [offset, setOffset] = useState(0);
  const payments = useQuery({
    queryKey: ["payments", user?.id, offset],
    queryFn: () =>
      request<Page<Payment>>(
        `/billing/v1/me/payments?limit=20&offset=${offset}`,
      ),
  });
  const methods = useQuery({
    queryKey: ["payment-methods", user?.id],
    queryFn: () =>
      request<Page<PaymentMethod>>("/billing/v1/me/payment-methods"),
  });
  return (
    <div className="stack">
      <div className="panel">
        <div className="panel-heading">
          <div>
            <h2>Payment history</h2>
            <p>
              Verified payment records, including refunds and unsuccessful
              attempts.
            </p>
          </div>
          <Icon name="wallet" />
        </div>
        {payments.isPending ? (
          <Loading />
        ) : payments.error ? (
          <ErrorState
            error={payments.error}
            retry={() => void payments.refetch()}
          />
        ) : payments.data?.data.length ? (
          <>
            <div className="data-list">
              {payments.data.data.map((payment) => (
                <div className="data-row" key={payment.id}>
                  <div className="data-row-main">
                    <h3>{payment.price?.product?.display_name || "Payment"}</h3>
                    <p>
                      {date(payment.created_at)} · {payment.rail}
                    </p>
                    <p className="truncate">{payment.id}</p>
                  </div>
                  <Badge
                    tone={
                      payment.status === "succeeded" ||
                      payment.status === "completed"
                        ? "success"
                        : payment.status === "failed"
                          ? "warning"
                          : "neutral"
                    }
                  >
                    {payment.status ||
                      (payment.refunded ? "Refunded" : "Recorded")}
                  </Badge>
                  <span className="amount">
                    {money(payment.amount, payment.currency)}
                  </span>
                </div>
              ))}
            </div>
            <div className="pagination inline-actions">
              <Button
                variant="ghost"
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - 20))}
              >
                Newer
              </Button>
              <Button
                variant="secondary"
                disabled={payments.data.data.length < 20}
                onClick={() => setOffset(offset + 20)}
              >
                Older
              </Button>
            </div>
          </>
        ) : (
          <EmptyState icon="wallet" title="No payments on this page." />
        )}
      </div>
      <div className="panel">
        <div className="panel-heading">
          <div>
            <h2>Saved payment methods</h2>
            <p>
              Card details are stored by Stripe. This site only receives a
              verified reference.
            </p>
          </div>
        </div>
        {methods.isPending ? (
          <Loading />
        ) : methods.error ? (
          <ErrorState error={methods.error} />
        ) : methods.data?.data.length ? (
          <div className="data-list">
            {methods.data.data.map((card) => (
              <div className="data-row" key={card.id}>
                <Icon name="wallet" />
                <div className="data-row-main">
                  <h3>
                    {card.card?.brand || "Card"} ending{" "}
                    {card.card?.last4 || "••••"}
                  </h3>
                  <p>
                    Expires {card.card?.exp_month}/{card.card?.exp_year}
                  </p>
                </div>
                <Badge>
                  {card.health?.active === false ? "Needs attention" : "Saved"}
                </Badge>
              </div>
            ))}
          </div>
        ) : (
          <p className="muted">
            No saved cards. You can securely add a card when joining a channel.
          </p>
        )}
      </div>
    </div>
  );
}
function SetupReturn({ id }: { id: string }) {
  const confirm = useMutation({
    mutationFn: () =>
      request<{ payment_method_id?: string; status: string }>(
        `/billing/v1/me/payment-methods/stripe-setup/${encodeURIComponent(id)}/confirm`,
        { method: "POST" },
      ),
  });
  const setup = useQuery({
    queryKey: ["setup-return", id],
    queryFn: () =>
      request<{ payment_method_id?: string; status: string }>(
        `/billing/v1/me/payment-methods/stripe-setup/${encodeURIComponent(id)}`,
      ),
  });
  const done = !!(
    setup.data?.payment_method_id || confirm.data?.payment_method_id
  );
  return (
    <div
      className={`notice ${done ? "notice-success" : ""}`}
      style={{ marginBottom: 24 }}
    >
      <Icon name={done ? "check" : "wallet"} />
      <div>
        <strong>
          {done ? "Card setup verified." : "Finish card verification"}
        </strong>
        <p>
          {done
            ? "Return to the channel to review and start your membership."
            : "Confirm the setup with the server after returning from Stripe. This does not start a membership."}
        </p>
        {!done && (
          <Button busy={confirm.isPending} onClick={() => confirm.mutate()}>
            Verify saved card
          </Button>
        )}
        {(setup.error || confirm.error) && (
          <p className="form-error">
            {setup.error?.message || confirm.error?.message}
          </p>
        )}
      </div>
    </div>
  );
}
function AccountSettings() {
  const auth = useAuth();
  const [open, setOpen] = useState(false);
  const client = useQueryClient();
  const remove = useMutation({
    mutationFn: (password: string) => authAPI.deleteAccount(password),
    onSuccess: () => {
      setSession(null);
      client.clear();
    },
  });
  return (
    <div className="panel stack">
      <h2>Account</h2>
      <p className="muted">
        Signed in as {auth.user?.username}
        {auth.user?.email && ` · ${auth.user.email}`}
      </p>
      <h3>Delete your account</h3>
      <p className="muted">
        Your account can be recovered for 30 days by signing in and explicitly
        confirming restoration. First transfer or delete any active channels for
        which you are the last owner. Purchases and financial history are
        retained independently.
      </p>
      <Button variant="danger" onClick={() => setOpen(true)}>
        Delete my account
      </Button>
      <Modal
        open={open}
        onOpenChange={setOpen}
        title="Schedule account deletion?"
        description="Confirm your current password. Recovery is available for 30 days; a deleted channel will not be restored with your account."
      >
        <form
          className="stack"
          onSubmit={(event) => {
            event.preventDefault();
            remove.mutate(
              String(new FormData(event.currentTarget).get("password")),
            );
          }}
        >
          <Field label="Current password">
            <input
              type="password"
              name="password"
              autoComplete="current-password"
              required
            />
          </Field>
          {remove.error && (
            <p className="form-error" role="alert">
              {remove.error.message}
            </p>
          )}
          <div className="form-actions">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
            >
              Keep account
            </Button>
            <Button type="submit" variant="danger" busy={remove.isPending}>
              Delete account
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
