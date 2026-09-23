import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { lazy, Suspense, useState } from "react";
import { getSessionGeneration, request } from "../api";
import { useAuth } from "../auth-context";
import { checkoutAttempt, finishCheckout } from "../attempts";
import {
  policyLabels,
  terminalCheckout,
  type Channel,
  type Checkout,
  type Post,
} from "../models";
import { money, date } from "../format";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Icon,
  Loading,
  Modal,
} from "../components/ui";
import { PostEditor } from "../components/post-editor";
import { MembershipDialog } from "../components/membership";
const PurchaseCheckout = lazy(() =>
  import("../components/purchase-checkout").then((module) => ({
    default: module.PurchaseCheckout,
  })),
);

function latestCheckout(user?: string) {
  try {
    return sessionStorage.getItem(`openrails-latest-checkout:${user}`) || "";
  } catch {
    return "";
  }
}
export function PostPage() {
  const { id = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [purchaseOpen, setPurchaseOpen] = useState(false);
  const [editor, setEditor] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [join, setJoin] = useState(false);
  const [selectedPrice, setSelectedPrice] = useState("");
  const post = useQuery({
    queryKey: ["post", id, auth.user?.id],
    queryFn: () => request<Post>(`/api/v1/posts/${id}`),
  });
  const channel = useQuery({
    queryKey: ["channel", post.data?.channel_id, auth.user?.id],
    queryFn: () =>
      request<Channel>(`/api/v1/channels/${post.data!.channel_id}`),
    enabled: !!post.data,
  });
  const offers = post.data?.offers?.filter((offer) => !offer.auto_renew) || [];
  const offer =
    offers.find((value) => value.price_id === selectedPrice) || offers[0];
  const remove = useMutation({
    mutationFn: () => request(`/api/v1/posts/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      void client.invalidateQueries();
      navigate(`/channels/${post.data?.channel_id}`);
    },
  });
  if (post.isPending) return <Loading />;
  if (post.error || !post.data)
    return (
      <ErrorState
        error={post.error || new Error("Post not found.")}
        retry={() => void post.refetch()}
      />
    );
  const item = post.data;
  const policy = item.access_policy;
  const mustJoin =
    policy === "membership" ||
    (policy === "members_ppv" && !channel.data?.has_membership);
  const canEdit = item.can_edit || channel.data?.can_edit;
  const action = () => {
    if (!auth.user) auth.openLogin();
    else if (mustJoin) setJoin(true);
    else setPurchaseOpen(true);
  };
  return (
    <>
      <div className="breadcrumb">
        <Link to="/">Explore</Link>
        <Icon name="chevron" size={13} />
        <Link to={`/channels/${item.channel_id}`}>
          {channel.data?.name || "Channel"}
        </Link>
        <Icon name="chevron" size={13} />
        <span>{item.title}</span>
      </div>
      <div className="reader-layout">
        <article className="reader-main">
          <span className="eyebrow">
            {channel.data?.name || "Independent channel"}
          </span>
          <h1 className="reader-title">{item.title}</h1>
          <div className="reader-meta">
            <span className="avatar">
              {(channel.data?.name || "O").slice(0, 1)}
            </span>
            <span>{channel.data?.name || "Channel story"}</span>
            <span>·</span>
            <span>{date(item.created_at)}</span>
            {item.purchased && <Badge tone="success">Purchased</Badge>}
          </div>
          {canEdit && (
            <div className="inline-actions" style={{ marginBottom: 25 }}>
              <Button variant="secondary" onClick={() => setEditor(true)}>
                <Icon name="edit" size={15} />
                Edit post
              </Button>
              <Button variant="ghost" onClick={() => setDeleting(true)}>
                Delete post
              </Button>
            </div>
          )}
          {item.can_read ? (
            <div className="article-body">
              {item.body || "This post has no body."}
            </div>
          ) : (
            <div className="locked-content">
              <div className="empty-icon">
                <Icon name="lock" size={27} />
              </div>
              <h2>This story is waiting for you.</h2>
              <p>
                {policy === "membership"
                  ? "Join this channel to read its included posts, now and in the future."
                  : policy === "members_ppv"
                    ? "You need an active membership to purchase this post. Once purchased, access remains yours after membership ends."
                    : "Buy this post once and keep reading access permanently."}
              </p>
              {offer || mustJoin ? (
                <Button onClick={action}>
                  {!auth.user
                    ? "Sign in to continue"
                    : mustJoin
                      ? "Join channel"
                      : `Buy for ${money(offer.unit_amount, offer.currency)}`}
                </Button>
              ) : (
                <p>This post is not currently for sale.</p>
              )}
            </div>
          )}
        </article>
        <aside className="panel purchase-panel">
          <Badge
            tone={
              item.purchased
                ? "success"
                : policy === "public"
                  ? "neutral"
                  : "brand"
            }
          >
            {item.purchased
              ? "Permanent purchased access"
              : policyLabels[policy]}
          </Badge>
          <h3>{item.can_read ? "You have access." : "Support the story."}</h3>
          <p>
            {item.purchased
              ? "This purchase remains yours after membership ends. Deleted or archived content may no longer be available."
              : policy === "public"
                ? "No account is required to read this post."
                : policy === "membership"
                  ? "Available while your channel membership is active."
                  : "A one-time purchase. No recurring charge for this post."}
          </p>
          {!item.can_read && offer && (
            <div className="purchase-price">
              {money(offer.unit_amount, offer.currency)} <small>one time</small>
            </div>
          )}
          {!item.can_read && (offer || mustJoin) && (
            <Button
              className="button-full"
              disabled={
                mustJoin &&
                (!channel.data ||
                  !channel.data.offers?.some((value) => value.auto_renew))
              }
              onClick={action}
            >
              {mustJoin ? "View membership" : "Review purchase"}
            </Button>
          )}
          <div className="purchase-note">
            <Icon name="lock" size={14} />
            <span>
              Stripe test payments. Access is granted only after verified
              payment confirmation.
            </span>
          </div>
        </aside>
      </div>
      <Modal
        open={purchaseOpen}
        onOpenChange={setPurchaseOpen}
        title="Review your purchase"
        description="One-time payment. Purchased reading access remains permanent after membership ends."
      >
        <div className="stack">
          <h3>{item.title}</h3>
          {offers.length > 1 && (
            <label className="field">
              <span>Choose price</span>
              <select
                value={offer?.price_id}
                onChange={(event) => setSelectedPrice(event.target.value)}
              >
                {offers.map((value) => (
                  <option key={value.price_id} value={value.price_id}>
                    {money(value.unit_amount, value.currency)}
                  </option>
                ))}
              </select>
            </label>
          )}
          {offer && (
            <div className="purchase-price">
              {money(offer.unit_amount, offer.currency)}
            </div>
          )}
          {offer && (
            <Suspense fallback={<Loading />}>
              <PurchaseCheckout
                key={`${getSessionGeneration()}:${offer.price_id}`}
                post={item}
                offer={offer}
                onComplete={() => {
                  setPurchaseOpen(false);
                  void post.refetch();
                }}
              />
            </Suspense>
          )}
        </div>
      </Modal>
      <PostEditor
        post={item}
        channelID={item.channel_id}
        open={editor}
        onClose={() => setEditor(false)}
      />
      <Modal
        open={deleting}
        onOpenChange={setDeleting}
        title="Delete this post?"
        description="The post is removed from the channel. Financial and purchased-access records remain in billing history."
      >
        <div className="form-actions">
          <Button variant="ghost" onClick={() => setDeleting(false)}>
            Keep post
          </Button>
          <Button
            variant="danger"
            busy={remove.isPending}
            onClick={() => remove.mutate()}
          >
            Delete post
          </Button>
        </div>
        {remove.error && (
          <p className="form-error" role="alert">
            {remove.error.message}
          </p>
        )}
      </Modal>
      {channel.data && (
        <MembershipDialog
          channel={channel.data}
          open={join}
          onClose={() => setJoin(false)}
        />
      )}
    </>
  );
}
export function CheckoutReturnPage() {
  const [search] = useSearchParams();
  const auth = useAuth();
  const id = search.get("checkout_id") || latestCheckout(auth.user?.id);
  const status = useQuery({
    queryKey: ["checkout", id, auth.user?.id],
    queryFn: async () => {
      const checkout = await request<Checkout>(
        `/api/v1/checkouts/${encodeURIComponent(id)}`,
      );
      if (["succeeded", "failed", "canceled"].includes(checkout.status))
        finishCheckout(checkout.id);
      return checkout;
    },
    enabled: !!id,
    refetchInterval: (query) =>
      terminalCheckout(query.state.data?.status) ? false : 1800,
  });
  const resume = useMutation({
    mutationFn: async () => {
      const previous = checkoutAttempt(id);
      if (!previous?.request)
        throw new Error(
          "The original purchase request is unavailable. Return to the post in the browser that started checkout.",
        );
      return request<Checkout>(previous.request.path, {
        method: "POST",
        headers: { "Idempotency-Key": previous.key },
        body: JSON.stringify(previous.request.body),
      });
    },
    onSuccess: (result) => {
      if (result.url && !terminalCheckout(result.status)) {
        const url = new URL(result.url);
        if (url.protocol !== "https:" || url.hostname !== "checkout.stripe.com")
          throw new Error("Unexpected checkout destination.");
        location.assign(url.href);
      } else void status.refetch();
    },
  });
  if (!id)
    return (
      <EmptyState
        icon="warning"
        title="No checkout reference in this browser."
        action={
          <Link className="button button-secondary" to="/me">
            Check my library
          </Link>
        }
      >
        Sign in using the same browser tab that started checkout, or check your
        verified purchase history.
      </EmptyState>
    );
  if (status.isPending) return <Loading />;
  if (status.error)
    return (
      <ErrorState error={status.error} retry={() => void status.refetch()} />
    );
  const value = status.data!;
  const complete = value.status === "succeeded";
  const ended = terminalCheckout(value.status);
  return (
    <div className="centered-page">
      <div
        className="empty-icon"
        style={{ color: complete ? "var(--success)" : undefined }}
      >
        <Icon
          name={complete ? "check" : ended ? "warning" : "wallet"}
          size={29}
        />
      </div>
      <span className="eyebrow">
        {complete
          ? "Purchase confirmed"
          : ended
            ? `Checkout ${value.status}`
            : "Payment confirmation"}
      </span>
      <h1 style={{ marginTop: 16 }}>
        {complete
          ? "It’s in your library."
          : ended
            ? "This attempt is not complete."
            : "We’re checking your payment."}
      </h1>
      <p>
        {complete
          ? "The server confirmed your payment. Your purchased access remains after membership ends."
          : ended
            ? "No successful purchase was confirmed for this attempt. Check your payment history before trying again; an expired provider page can still require reconciliation."
            : "Waiting for the provider’s verified result. Closing Stripe is not a payment confirmation or a cancellation receipt. Do not start another payment while this is unresolved."}
      </p>
      {!ended && (
        <div className="loading">
          <span className="spinner" />
          Checking securely…
        </div>
      )}
      {value.status === "created" && (
        <div className="inline-actions">
          <Button busy={resume.isPending} onClick={() => resume.mutate()}>
            Continue the same checkout
          </Button>
        </div>
      )}
      {resume.error && (
        <p className="form-error" role="alert">
          {resume.error.message}
        </p>
      )}
      <div className="inline-actions">
        <Link to="/me?tab=library" className="button button-primary">
          Open my library
          <Icon name="arrow" size={16} />
        </Link>
        <Link to="/" className="button button-secondary">
          Explore stories
        </Link>
        {!complete && (
          <Button variant="ghost" onClick={() => void status.refetch()}>
            Check again
          </Button>
        )}
      </div>
    </div>
  );
}
