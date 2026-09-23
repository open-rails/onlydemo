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
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Add01Icon,
  Book02Icon,
  CreditCardIcon,
  LinkSquare02Icon,
  Settings02Icon,
  Tick02Icon,
  UserGroupIcon,
  Wallet01Icon,
} from "@hugeicons/core-free-icons";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  EmptyState,
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
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
      <Tabs
        value={tab}
        onValueChange={(value) => setSearch({ tab: String(value) })}
        className="my-4"
      >
        <TabsList variant="line" aria-label="Account sections" className="w-full overflow-x-auto">
          {Object.entries(titles).map(([key, label]) => (
            <TabsTrigger key={key} value={key}>
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
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
                    variant="outline"
                    disabled={summary.isFetchingNextPage}
                    onClick={() => void summary.fetchNextPage()}
                  >
                    {summary.isFetchingNextPage && (
                      <Spinner data-icon="inline-start" />
                    )}
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
                <Button nativeButton={false} render={<Link to="/channels/new" />}>
                  <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
                  Create channel
                </Button>
              </div>
              {current?.manageable_channels?.length ? (
                <div className="channel-grid">
                  {current.manageable_channels.map((channel) => (
                    <ChannelCard channel={channel} key={channel.id} />
                  ))}
                </div>
              ) : (
                <EmptyState
                  icon={Settings02Icon}
                  title="No editorial channels yet."
                >
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
      icon={Book02Icon}
      title={
        searching ? "No purchases on this page." : "Nothing unlocked yet."
      }
      action={
        !searching ? (
          <Button nativeButton={false} render={<Link to="/channels" />}>
            Explore creators
          </Button>
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
    mutationFn: async ({
      id,
      action,
      feedback,
    }: {
      id: string;
      action: "cancel" | "resume";
      feedback?: string;
    }) => {
      await request(`/billing/v1/me/subscriptions/${id}/${action}`, {
        method: "POST",
        body: JSON.stringify(feedback ? { feedback } : {}),
      });
      // The server queues the change (202); wait until it is applied.
      for (let attempt = 0; attempt < 20; attempt++) {
        const page = await request<Page<Subscription>>(
          "/billing/v1/me/subscriptions?limit=100",
        );
        const current = page.data.find((value) => value.id === id);
        if (!current || !!current.cancel_scheduled === (action === "cancel"))
          return;
        await new Promise((resolve) => setTimeout(resolve, 1500));
      }
      throw new Error(
        "The change is still processing. Refresh in a moment to see it.",
      );
    },
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
        <Card>
          <CardHeader>
            <CardTitle>Channel memberships</CardTitle>
            <CardDescription>
              Cancellation follows the server’s agreement and access end date.
              Purchased posts remain yours.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
          <div className="data-list">
            {subscriptions.data.data.map((subscription) => (
              <div className="data-row" key={subscription.id}>
                <span className="avatar">
                  <HugeiconsIcon icon={UserGroupIcon} size={16} />
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
                  variant="secondary"
                  className={
                    subscription.cancel_scheduled
                      ? "bg-warning/10 text-warning"
                      : subscription.status === "active"
                        ? "bg-success/10 text-success"
                        : undefined
                  }
                >
                  {subscription.cancel_scheduled
                    ? "Cancellation scheduled"
                    : subscription.status}
                </Badge>
                <div className="data-row-actions">
                  {subscription.cancel_portal_url ? (
                    <Button
                      variant="outline"
                      nativeButton={false}
                      render={
                        <a
                          target="_blank"
                          rel="noopener noreferrer"
                          href={subscription.cancel_portal_url}
                        />
                      }
                    >
                      Manage cancellation
                      <HugeiconsIcon icon={LinkSquare02Icon} data-icon="inline-end" />
                    </Button>
                  ) : (
                    !subscription.cancel_scheduled &&
                    ["active", "pending", "past_due"].includes(
                      subscription.status,
                    ) && (
                      <Button
                        variant="outline"
                        onClick={() => setCancel(subscription)}
                      >
                        Cancel membership
                      </Button>
                    )
                  )}
                  {subscription.resumable && (
                    <Button
                      variant="outline"
                      disabled={change.isPending}
                      onClick={() =>
                        change.mutate({ id: subscription.id, action: "resume" })
                      }
                    >
                      {change.isPending && <Spinner data-icon="inline-start" />}
                      Resume
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
          {!cancel && <FormError>{change.error?.message}</FormError>}
          </CardContent>
        </Card>
      ) : (
        <EmptyState
          icon={UserGroupIcon}
          title="No subscriptions yet."
          action={
            <Button
              variant="outline"
              nativeButton={false}
              render={<Link to="/channels" />}
            >
              Explore creators
            </Button>
          }
        >
          Join a channel to read its included posts while subscribed.
        </EmptyState>
      )}
      <Dialog
        open={!!cancel}
        onOpenChange={(value) => {
          if (!value) setCancel(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cancel this membership?</DialogTitle>
            <DialogDescription>
              Your purchased posts remain yours. The server will confirm when
              included membership access ends.
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="cancel-feedback">
                  Reason for cancelling
                </FieldLabel>
                <Textarea
                  id="cancel-feedback"
                  name="feedback"
                  minLength={4}
                  maxLength={500}
                  required
                  placeholder="Tell the creator why you’re leaving."
                />
              </Field>
              <FormError>{change.error?.message}</FormError>
              <DialogFooter>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => setCancel(null)}
                >
                  Keep membership
                </Button>
                <Button
                  variant="destructive"
                  type="submit"
                  disabled={change.isPending}
                >
                  {change.isPending && <Spinner data-icon="inline-start" />}
                  Confirm cancellation
                </Button>
              </DialogFooter>
            </FieldGroup>
          </form>
        </DialogContent>
      </Dialog>
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
  const subscriptions = useQuery({
    queryKey: ["subscriptions", user?.id],
    queryFn: () =>
      request<Page<Subscription>>("/billing/v1/me/subscriptions?limit=100"),
  });
  const membershipName = (id?: string) =>
    subscriptions.data?.data.find((value) => value.id === id)?.product
      ?.display_name;
  const methods = useQuery({
    queryKey: ["payment-methods", user?.id],
    queryFn: () =>
      request<Page<PaymentMethod>>("/billing/v1/me/payment-methods"),
  });
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Payment history</CardTitle>
          <CardDescription>
            Verified payment records, including refunds and unsuccessful
            attempts.
          </CardDescription>
          <CardAction>
            <HugeiconsIcon icon={Wallet01Icon} />
          </CardAction>
        </CardHeader>
        <CardContent>
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
                    <h3>
                      {payment.subscription_id ||
                      payment.price?.type === "recurring"
                        ? membershipName(payment.subscription_id) ||
                          "Channel membership"
                        : payment.price
                          ? "Post purchase"
                          : "Payment"}
                    </h3>
                    <p>
                      {date(payment.created_at)} · {payment.rail}
                    </p>
                    <p className="truncate">{payment.id}</p>
                  </div>
                  <Badge
                    variant="secondary"
                    className={
                      payment.status === "succeeded" ||
                      payment.status === "completed"
                        ? "bg-success/10 text-success"
                        : payment.status === "failed"
                          ? "bg-warning/10 text-warning"
                          : undefined
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
                variant="outline"
                disabled={payments.data.data.length < 20}
                onClick={() => setOffset(offset + 20)}
              >
                Older
              </Button>
            </div>
          </>
        ) : (
          <EmptyState icon={Wallet01Icon} title="No payments on this page." />
        )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Saved payment methods</CardTitle>
          <CardDescription>
            Card details are stored by Stripe. This site only receives a
            verified reference.
          </CardDescription>
        </CardHeader>
        <CardContent>
        {methods.isPending ? (
          <Loading />
        ) : methods.error ? (
          <ErrorState error={methods.error} />
        ) : methods.data?.data.length ? (
          <div className="data-list">
            {methods.data.data.map((card) => (
              <div className="data-row" key={card.id}>
                <HugeiconsIcon icon={CreditCardIcon} />
                <div className="data-row-main">
                  <h3>
                    {card.card?.brand || "Card"} ending{" "}
                    {card.card?.last4 || "••••"}
                  </h3>
                  <p>
                    Expires {card.card?.exp_month}/{card.card?.exp_year}
                  </p>
                </div>
                <Badge variant="secondary">
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
        </CardContent>
      </Card>
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
    <Alert className="mb-6">
      <HugeiconsIcon
        icon={done ? Tick02Icon : Wallet01Icon}
        className={done ? "text-success" : undefined}
      />
      <AlertTitle>
        {done ? "Card setup verified." : "Finish card verification"}
      </AlertTitle>
      <AlertDescription>
        <p>
          {done
            ? "Return to the channel to review and start your membership."
            : "Confirm the setup with the server after returning from Stripe. This does not start a membership."}
        </p>
        {!done && (
          <Button disabled={confirm.isPending} onClick={() => confirm.mutate()}>
            {confirm.isPending && <Spinner data-icon="inline-start" />}
            Verify saved card
          </Button>
        )}
        <FormError>{setup.error?.message || confirm.error?.message}</FormError>
      </AlertDescription>
    </Alert>
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
    <Card>
      <CardHeader>
        <CardTitle>Account</CardTitle>
        <CardDescription>
          Signed in as {auth.user?.username}
          {auth.user?.email && ` · ${auth.user.email}`}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <h3>Delete your account</h3>
        <p className="muted">
          Your account can be recovered for 30 days by signing in and
          explicitly confirming restoration. First transfer or delete any
          active channels for which you are the last owner. Purchases and
          financial history are retained independently.
        </p>
      </CardContent>
      <CardFooter>
        <Button variant="destructive" onClick={() => setOpen(true)}>
          Delete my account
        </Button>
      </CardFooter>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Schedule account deletion?</DialogTitle>
            <DialogDescription>
              Confirm your current password. Recovery is available for 30
              days; a deleted channel will not be restored with your account.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              remove.mutate(
                String(new FormData(event.currentTarget).get("password")),
              );
            }}
          >
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="delete-password">
                  Current password
                </FieldLabel>
                <Input
                  id="delete-password"
                  type="password"
                  name="password"
                  autoComplete="current-password"
                  required
                />
              </Field>
              <FormError>{remove.error?.message}</FormError>
              <DialogFooter>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => setOpen(false)}
                >
                  Keep account
                </Button>
                <Button
                  type="submit"
                  variant="destructive"
                  disabled={remove.isPending}
                >
                  {remove.isPending && <Spinner data-icon="inline-start" />}
                  Delete account
                </Button>
              </DialogFooter>
            </FieldGroup>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
