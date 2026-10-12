import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { useEffect, useState } from "react";
import { APIError } from "../api";
import { loadPost, posts as postWrites, resolveCode } from "../content";
import { NotFoundPage } from "../App";
import { useAuth } from "../session";
import { visibilityLabels } from "../models";
import { money } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  ArrowLeft02Icon,
  Delete02Icon,
  PencilEdit02Icon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
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
import { PostMediaEditor } from "../components/post-media";
import { PostView } from "../components/post-view";
import { canJoin, membershipOffer } from "../channels";
import {
  subscribeLabel,
  unlockLabel,
  useJoinFree,
  usePay,
} from "../components/pay";
import { useCanonicalContent } from "@openrails/contentkit-ui/react";
import { channelPath, parseCode, postLink, postPath } from "../paths";
export function PostPage() {
  const params = useParams();
  const code = params.id ? "" : (parseCode(params.code ?? "") ?? "");
  const byID = params.id ?? "";
  const location = useLocation();
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const pay = usePay();
  const [editor, setEditor] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const loaded = useQuery({
    queryKey: ["post", code || byID, auth.user?.id],
    queryFn: async () => loadPost(byID || (await resolveCode(code)), !!auth.user),
    enabled: !!(code || byID),
  });
  const post = { ...loaded, data: loaded.data?.post };
  const channel = { data: loaded.data?.channel ?? undefined };
  const offer = post.data?.offers?.find((value) => value.kind !== "recurring");
  const joinFree = useJoinFree(post.data?.channel_id || "");
  // The slug is decoration: keep the address bar and head on the canonical URL.
  useCanonicalContent(post.data && postLink(post.data), {
    title: post.data?.title || undefined,
    location: location.pathname + location.search + location.hash,
  });
  const pageTitle = post.data && (post.data.title || `Post by @${post.data.channel_slug}`);
  useEffect(() => {
    if (!pageTitle) return;
    document.title = `${pageTitle} · OnlyDemo`;
    return () => {
      document.title = "OnlyDemo";
    };
  }, [pageTitle]);
  const remove = useMutation({
    mutationFn: () => postWrites.remove(post.data!.id),
    onSuccess: () => {
      void client.invalidateQueries();
      navigate(channelPath(post.data?.channel_slug || ""));
    },
  });
  if (!code && !byID) return <NotFoundPage />;
  if (post.isPending) return <Loading />;
  if (post.error instanceof APIError && post.error.status === 404)
    return <NotFoundPage />;
  if (post.error || !post.data)
    return (
      <ErrorState
        error={post.error || new Error("Post not found.")}
        retry={() => void loaded.refetch()}
      />
    );
  const item = post.data;
  const policy = item.access_policy;
  const mustJoin =
    policy === "membership" ||
    (policy === "members_ppv" && !channel.data?.membership.member);
  const joinable = canJoin(channel.data);
  const canEdit = item.can_edit || channel.data?.can_edit;
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
        <PostView
          post={item}
          variant="full"
          handle={channel.data?.slug}
          membership={channel.data?.membership}
          badges={
            <>
              {item.purchased && <Badge className="bg-success/10 text-success">Purchased</Badge>}{" "}
            </>
          }
          controls={
            canEdit && (
              <div className="feed-controls">
                <Button variant="ghost" size="icon-sm" aria-label="Edit post" title="Edit post" onClick={() => setEditor(true)}>
                  <HugeiconsIcon icon={PencilEdit02Icon} />
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label="Delete post" title="Delete post" onClick={() => setDeleting(true)}>
                  <HugeiconsIcon icon={Delete02Icon} />
                </Button>
              </div>
            )
          }
          editor={canEdit && <PostMediaEditor post={item} />}
        />
        <aside className="panel purchase-panel">
          <dl className="post-facts">
            <dt>Visibility</dt>
            <dd>{visibilityLabels[policy]}</dd>
            {policy !== "public" && policy !== "membership" && (
              <>
                <dt>Price</dt>
                <dd>{offer ? money(offer.unit_amount, offer.currency) : "—"}</dd>
              </>
            )}
            {item.purchased && (
              <>
                <dt>Access</dt>
                <dd className="text-success">Purchased</dd>
              </>
            )}
          </dl>
          {!item.can_read && (offer || mustJoin) && (
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
        </aside>
      </div>
      <PostEditor
        post={item}
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
