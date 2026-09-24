import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useEffect, useState, type CSSProperties } from "react";
import { APIError, request } from "../api";
import { NotFoundPage } from "../App";
import { useAuth } from "../session";
import { policyLabels, type Channel, type Post } from "../models";
import { money, date } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  ArrowLeft02Icon,
  PencilEdit02Icon,
  SquareLock02Icon,
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
import { Spinner } from "@/components/ui/spinner";
import {
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
import { PostEditor } from "../components/post-editor";
import { PostGallery, PostMediaEditor } from "../components/post-media";
import { Avatar } from "../components/cards";
import { canJoin, hue, membershipOffer } from "../channels";
import {
  subscribeLabel,
  unlockLabel,
  useJoinFree,
  usePay,
} from "../components/pay";
import { channelPath, postPath } from "../paths";
export function PostPage() {
  const { channel: channelSlug = "", post: postSlug = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const pay = usePay();
  const [editor, setEditor] = useState(false);
  const [deleting, setDeleting] = useState(false);
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
  const offer = post.data?.offers?.find((value) => !value.auto_renew);
  const joinFree = useJoinFree(post.data?.channel_id || "");
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
  const membership = membershipOffer(channel.data);
  const freeJoin = mustJoin && !!channel.data?.membership.free;
  const cta = mustJoin
    ? freeJoin
      ? "Join free to unlock"
      : membership
        ? subscribeLabel(membership)
        : "Membership price pending"
    : offer
      ? unlockLabel(offer)
      : "";
  // One click opens the payment modal over this page; sign-in happens there.
  const action = () => {
    if (freeJoin) void joinFree.join();
    else if (mustJoin && membership)
      pay({ kind: "membership", id: item.channel_id, merchant: creator, offer: membership });
    else if (!mustJoin && offer)
      pay({ kind: "post", id: item.id, merchant: creator, offer });
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
              <Avatar name={creator} seed={item.channel_id} image={item.channel_avatar} />
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
          {canEdit && (
            <PostMediaEditor postID={item.id} channel={channel.data?.can_manage ? item.channel_id : undefined} />
          )}
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
                  <Button
                    size="lg"
                    className="w-full"
                    disabled={joinFree.pending || (mustJoin && !freeJoin && !membership)}
                    onClick={action}
                  >
                    <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
                    {cta}
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
              disabled={joinFree.pending || (mustJoin && (!joinable || (!freeJoin && !membership)))}
              onClick={action}
            >
              {mustJoin && !joinable ? "Membership closed" : cta}
            </Button>
          )}
          {joinFree.error && <FormError>{joinFree.error}</FormError>}
          <div className="purchase-note">
            <HugeiconsIcon icon={SquareLock02Icon} size={14} />
            <span>
              Sandbox test payments. Access is granted only after verified
              payment confirmation.
            </span>
          </div>
        </aside>
      </div>
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
    </>
  );
}
