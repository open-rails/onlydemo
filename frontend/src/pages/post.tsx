import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { lazy, Suspense, useEffect, useState, type CSSProperties } from "react";
import { APIError, sessionKey, request } from "../api";
import { NotFoundPage } from "../App";
import { useAuth } from "../session";
import { checkoutAttempt, finishCheckout } from "../attempts";
import {
  policyLabels,
  terminalCheckout,
  type Channel,
  type Checkout,
  type Post,
} from "../models";
import { money, date } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Alert02Icon,
  ArrowLeft02Icon,
  ArrowRight02Icon,
  PencilEdit02Icon,
  SquareLock02Icon,
  Tick02Icon,
  Wallet01Icon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { PolicyBadge } from "../components/policy-badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import {
  EmptyState,
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
import { PostEditor } from "../components/post-editor";
import { PostGallery, PostMediaEditor } from "../components/post-media";
import { Avatar } from "../components/cards";
import { canJoin, hue } from "../channels";
import { MembershipDialog } from "../components/membership";
import { channelPath, postPath } from "../paths";
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
  const { channel: channelSlug = "", post: postSlug = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [purchaseOpen, setPurchaseOpen] = useState(false);
  const [editor, setEditor] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [join, setJoin] = useState(false);
  const [selectedPrice, setSelectedPrice] = useState("");
  const post = useQuery({
    queryKey: ["post", channelSlug, postSlug, auth.user?.id],
    queryFn: () =>
      request<Post>(
        `/api/v1/channels/${encodeURIComponent(channelSlug)}/posts/${encodeURIComponent(postSlug)}`,
      ),
    refetchInterval: (query) =>
      query.state.data?.offer_status === "pending" ? 2000 : false,
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
  const canonical = post.data && postPath(post.data);
  useEffect(() => {
    // A former channel slug still resolves; show the current URL.
    if (canonical && canonical !== location.pathname)
      navigate(canonical, { replace: true });
  }, [canonical, navigate]);
  const remove = useMutation({
    mutationFn: () =>
      request(`/api/v1/posts/${post.data!.id}`, { method: "DELETE" }),
    onSuccess: () => {
      void client.invalidateQueries();
      navigate(channelPath(post.data?.channel_slug || ""));
    },
  });
  if (post.isPending) return <Loading />;
  if (post.error instanceof APIError && post.error.status === 404)
    return <NotFoundPage />;
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
    (policy === "members_ppv" && !channel.data?.membership.member);
  const joinable = canJoin(channel.data);
  const canEdit = item.can_edit || channel.data?.can_edit;
  const pricePending = item.offer_status === "pending" && !mustJoin;
  const creator = channel.data?.name || item.channel_name || "Creator";
  const action = () => {
    if (!auth.user) auth.openLogin();
    else if (mustJoin) setJoin(true);
    else setPurchaseOpen(true);
  };
  return (
    <>
      <div className="page-title">
        <Button
          variant="ghost"
          size="icon"
          aria-label="Back to creator"
          nativeButton={false}
          render={<Link to={channelPath(item.channel_slug)} />}
        >
          <HugeiconsIcon icon={ArrowLeft02Icon} />
        </Button>
        <h1>Post</h1>
      </div>
      <div className="reader-layout">
        <article className="feed-card reader-main">
          <header className="feed-head">
            <Link to={channelPath(item.channel_slug)} className="feed-author">
              <Avatar name={creator} seed={item.channel_id} src={item.channel_avatar_url} />
              <span>
                <strong>
                  {creator}
                </strong>
                {channel.data && <small>@{channel.data.slug}</small>}
              </span>
            </Link>
            <span className="feed-date">
              <PolicyBadge policy={policy} offer={offer} />{" "}
              {item.purchased && (
                <Badge className="bg-success/10 text-success">Purchased</Badge>
              )}{" "}
              {canEdit && item.offer_status === "pending" && (
                <Badge className="bg-warning/10 text-warning">Price pending</Badge>
              )}{" "}
              {date(item.created_at)}
            </span>
          </header>
          <div className="feed-text">
            <h2 className="reader-title">{item.title}</h2>
            {canEdit && (
              <div className="inline-actions">
                <Button variant="outline" size="sm" onClick={() => setEditor(true)}>
                  <HugeiconsIcon icon={PencilEdit02Icon} data-icon="inline-start" />
                  Edit post
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setDeleting(true)}>
                  Delete post
                </Button>
              </div>
            )}
            {item.can_read && (
              <div className="article-body">
                {item.body || "This post has no body."}
              </div>
            )}
          </div>
          <PostGallery postID={item.id} viewer={auth.user?.id} />
          {canEdit && <PostMediaEditor postID={item.id} />}
          {!item.can_read && (
            <div className="locked-content">
              <div
                className="feed-media"
                style={{ "--h": hue(item.channel_id) } as CSSProperties}
              >
                <span className="lock-badge">
                  <HugeiconsIcon icon={SquareLock02Icon} size={30} />
                </span>
              </div>
              <div className="feed-unlock">
                <p>
                  {policy === "membership"
                    ? "Subscribe to this creator to unlock this post and everything included with membership."
                    : policy === "members_ppv"
                      ? "Subscribers can unlock this post. Once unlocked, it stays yours even after your subscription ends."
                      : "Unlock this post once and keep it forever."}
                </p>
                {pricePending ? (
                  <Button size="lg" className="w-full" disabled>
                    Price pending
                  </Button>
                ) : mustJoin && channel.data && !joinable ? (
                  <p>Membership is closed to new members.</p>
                ) : offer || mustJoin ? (
                  <Button size="lg" className="w-full" onClick={action}>
                    <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
                    {!auth.user
                      ? "Sign in to unlock"
                      : mustJoin
                        ? channel.data?.membership.free
                          ? "Join free to unlock"
                          : "Subscribe to unlock"
                        : `Unlock for ${money(offer.unit_amount, offer.currency)}`}
                  </Button>
                ) : (
                  <p>This post is not currently for sale.</p>
                )}
              </div>
            </div>
          )}
        </article>
        <aside className="panel purchase-panel">
          <Badge
            variant={
              item.purchased || policy === "public" ? "secondary" : "default"
            }
            className={item.purchased ? "bg-success/10 text-success" : undefined}
          >
            {item.purchased
              ? "Permanent purchased access"
              : policyLabels[policy]}
          </Badge>
          <h3>{item.can_read ? "You have access." : "Support the creator."}</h3>
          <p>
            {item.purchased
              ? "This purchase remains yours after membership ends. Deleted or archived content may no longer be available."
              : policy === "public"
                ? "No account is required to read this post."
                : policy === "membership"
                  ? "Available while your channel membership is active."
                  : "A one-time purchase. No recurring charge for this post."}
          </p>
          {(!item.can_read || canEdit) && pricePending && (
            <div className="purchase-price">
              <small>Price pending</small>
            </div>
          )}
          {(!item.can_read || canEdit) && !pricePending && offer && (
            <div className="purchase-price">
              {money(offer.unit_amount, offer.currency)} <small>one time</small>
            </div>
          )}
          {!item.can_read && !pricePending && (offer || mustJoin) && (
            <Button
              size="lg"
              className="w-full"
              disabled={mustJoin && !joinable}
              onClick={action}
            >
              {mustJoin
                ? joinable
                  ? "View membership"
                  : "Membership closed"
                : "Review purchase"}
            </Button>
          )}
          <div className="purchase-note">
            <HugeiconsIcon icon={SquareLock02Icon} size={14} />
            <span>
              Sandbox test payments. Access is granted only after verified
              payment confirmation.
            </span>
          </div>
        </aside>
      </div>
      <Dialog open={purchaseOpen} onOpenChange={setPurchaseOpen}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Review your purchase</DialogTitle>
          <DialogDescription>
            One-time payment. Purchased reading access remains permanent after
            membership ends.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <h3>{item.title}</h3>
          {offers.length > 1 && (
            <Field>
              <FieldLabel htmlFor="purchase-price">Choose price</FieldLabel>
              <NativeSelect
                id="purchase-price"
                value={offer?.price_id}
                onChange={(event) => setSelectedPrice(event.target.value)}
              >
                {offers.map((value) => (
                  <NativeSelectOption key={value.price_id} value={value.price_id}>
                    {money(value.unit_amount, value.currency)}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
          )}
          {offer && (
            <div className="purchase-price">
              {money(offer.unit_amount, offer.currency)}
            </div>
          )}
          {offer && (
            <Suspense fallback={<Loading />}>
              <PurchaseCheckout
                key={`${sessionKey()}:${offer.price_id}`}
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
        </DialogContent>
      </Dialog>
      <PostEditor
        post={item}
        channelID={item.channel_id}
        hasMembership={channel.data?.membership.status !== "none"}
        open={editor}
        onClose={() => setEditor(false)}
        onSaved={(saved) => navigate(postPath(saved), { replace: true })}
      />
      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this post?</DialogTitle>
            <DialogDescription>
              The post is removed from the channel. Financial and
              purchased-access records remain in billing history.
            </DialogDescription>
          </DialogHeader>
          <FormError>{remove.error?.message}</FormError>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep post
            </Button>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending && <Spinner data-icon="inline-start" />}
              Delete post
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
// Checkouts name the purchased post or channel by stable id; its current URL
// is resolved here so slug renames never strand a return.
function useReturnTarget(search: URLSearchParams) {
  const postID = search.get("post");
  const channelID = search.get("channel");
  const target = useQuery({
    queryKey: ["checkout-target", postID, channelID],
    queryFn: async () =>
      postID
        ? postPath(await request<Post>(`/api/v1/posts/${encodeURIComponent(postID)}`))
        : channelPath(
            (await request<Channel>(`/api/v1/channels/${encodeURIComponent(channelID!)}`)).slug,
          ),
    enabled: !!(postID || channelID),
  });
  return { path: target.data, label: postID ? "Open post" : "Open channel" };
}
export function CheckoutReturnPage() {
  const [search] = useSearchParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const target = useReturnTarget(search);
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
        if (url.protocol !== "https:")
          throw new Error("Unexpected checkout destination.");
        location.assign(url.href);
      } else void status.refetch();
    },
  });
  const complete = status.data?.status === "succeeded";
  useEffect(() => {
    if (complete && target.path) navigate(target.path, { replace: true });
  }, [complete, target.path, navigate]);
  const back = target.path && (
    <Button variant="outline" nativeButton={false} render={<Link to={target.path} />}>
      {target.label}
    </Button>
  );
  if (!id)
    return (
      <EmptyState
        icon={Alert02Icon}
        title="No checkout reference in this browser."
        action={
          <div className="inline-actions">
            <Button variant="outline" nativeButton={false} render={<Link to="/me" />}>
              View purchased
            </Button>
            {back}
          </div>
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
  const ended = terminalCheckout(value.status);
  return (
    <div className="centered-page">
      <EmptyState
        icon={complete ? Tick02Icon : ended ? Alert02Icon : Wallet01Icon}
        title={
          complete
            ? "Unlocked."
            : ended
              ? "This attempt is not complete."
              : "We’re checking your payment."
        }
        action={
          <>
            {!ended && <Loading label="Checking securely…" />}
            {value.status === "created" && (
              <Button disabled={resume.isPending} onClick={() => resume.mutate()}>
                {resume.isPending && <Spinner data-icon="inline-start" />}
                Continue the same checkout
              </Button>
            )}
            <FormError>{resume.error?.message}</FormError>
            <div className="inline-actions">
              <Button nativeButton={false} render={<Link to="/me?tab=library" />}>
                View purchased
                <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
              </Button>
              {back || (
                <Button variant="outline" nativeButton={false} render={<Link to="/" />}>
                  Back home
                </Button>
              )}
              {!complete && (
                <Button variant="ghost" onClick={() => void status.refetch()}>
                  Check again
                </Button>
              )}
            </div>
          </>
        }
      >
        <span className="eyebrow">
          {complete
            ? "Purchase confirmed"
            : ended
              ? `Checkout ${value.status}`
              : "Payment confirmation"}
        </span>{" "}
        {complete
          ? "The server confirmed your payment. Your purchased access remains after membership ends."
          : ended
            ? "No successful purchase was confirmed for this attempt. Check your payment history before trying again; an expired provider page can still require reconciliation."
            : "Waiting for the provider’s verified result. Closing the payment page is not a payment confirmation or a cancellation receipt. Do not start another payment while this is unresolved."}
      </EmptyState>
    </div>
  );
}
