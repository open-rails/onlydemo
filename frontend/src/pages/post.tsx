import { toast } from "sonner";
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { Comments, ContentEditor, ChannelAvatar, RepostButton, MediaGallery, Paywall } from "@openrails/contentkit-ui";
import { useCanonicalContent, useContentDetail } from "@openrails/contentkit-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { ArrowLeft02Icon, Delete02Icon, PencilEdit02Icon } from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { NotFoundPage } from "../App";
import { ErrorState, FormError, Loading } from "../components/states";
import { useCheckout } from "../components/pay";
import { contentkit } from "../media";
import { request } from "../api";
import { useChannelRoles } from "../roles";
import { ProductSettings } from "../components/product-settings";
import { dateTime } from "../format";
import { channelPath, contentURLs, editPostPath, parseCode, postLink, postPath } from "../paths";

// A post's content id, from its language page or the editor's id route.
function usePostID() {
  const params = useParams();
  const supported = !params.language || contentURLs.languages.includes(params.language);
  const code = params.id ? null : parseCode(params.code ?? "");
  const resolved = useQuery({
    queryKey: ["code", code, params.language],
    queryFn: () => contentkit.codes.resolve(code!, { lang: params.language }),
    enabled: !!code && supported,
  });
  return { id: params.id ?? (resolved.data?.content_kind === "post" ? resolved.data.content_id : null), missing: !params.id && (!code || !supported || !!resolved.error || (resolved.data && resolved.data.content_kind !== "post")), loading: resolved.isPending && !!code && supported };
}

export function PostPage() {
  const post = usePostID();
  const detail = useContentDetail(post.id);
  const location = useLocation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [error, setError] = useState("");
  const content = detail.content;
  const owner = content?.channel;
  const checkout = useCheckout(owner?.name ?? "Creator");
  // The slug is decoration: keep the address bar and head on the canonical URL.
  useCanonicalContent(content && content.code ? postLink({ code: content.code, slug: content.slug ?? "" }) : undefined, {
    title: content?.title || undefined,
    location: location.pathname + location.search + location.hash,
  });
  useEffect(() => {
    if (!content?.title) return;
    document.title = `${content.title} · OnlyDemo`;
    return () => {
      document.title = "OnlyDemo";
    };
  }, [content?.title]);
  if (post.missing) return <NotFoundPage />;
  // Reads reload on every change; show Loading only before the first one.
  if (post.loading || (detail.loading && !detail.content)) return <Loading />;
  if (detail.error?.status === 404) return <NotFoundPage />;
  if (detail.error || !content) return <ErrorState error={detail.error ?? new Error("Post not found.")} retry={detail.reload} />;
  const remove = async () => {
    setRemoving(true);
    setError("");
    try {
      await request(`/api/v1/products/contents/${content.id}`, {method: "DELETE"});
      for (const version of content.versions) await contentkit.contents.removeVersion(content.id, version.id);
      await client.invalidateQueries();
      navigate(owner ? channelPath(owner) : "/");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not delete the post.");
    } finally {
      setRemoving(false);
    }
  };
  const version = detail.version;
  return (
    <>
      <div className="page-title">
        <Button variant="ghost" size="icon" aria-label="Back to creator" nativeButton={false} render={<Link to={owner ? channelPath(owner) : "/"} />}>
          <HugeiconsIcon icon={ArrowLeft02Icon} />
        </Button>
        <h1>Post</h1>
      </div>
      <div className="reader-layout">
        <article className="feed-card reader-main" data-variant="full">
          <header className="feed-head">
            {owner && (
              <Link to={channelPath(owner)} className="feed-author">
                <ChannelAvatar channel={owner} className="avatar" />
                <span>
                  <strong>{owner.name}</strong>
                  <small>@{owner.slug}</small>
                </span>
              </Link>
            )}
            <div className="feed-meta">
              <span className="feed-date">
                {content.state === "deleted" && <Badge variant="outline">Deleted by its creator</Badge>}{" "}
                <Link to={postPath({ code: content.code, slug: content.slug ?? "", path: content.path })}>
                  <time dateTime={content.published_at ?? content.created_at}>{dateTime(content.published_at ?? content.created_at)}</time>
                </Link>
              </span>
              {content.editor && content.state !== "deleted" && (
                <div className="feed-controls">
                  <Button variant="ghost" size="icon-sm" aria-label="Edit post" title="Edit post" nativeButton={false} render={<Link to={editPostPath(content.id)} />}>
                    <HugeiconsIcon icon={PencilEdit02Icon} />
                  </Button>
                  <Button variant="ghost" size="icon-sm" aria-label="Delete post" title="Delete post" onClick={() => setDeleting(true)}>
                    <HugeiconsIcon icon={Delete02Icon} />
                  </Button>
                </div>
              )}
            </div>
          </header>
          <RepostButton contentId={content.id} originalChannelId={content.channel_id} />
          <div className="feed-text">
            {content.title && <h2 className="reader-title">{content.title}</h2>}
            {/* The body is the viewer's once they open the post; the description is its teaser. */}
            <div className="article-body">{content.body || content.description}</div>
          </div>
          {version && <MediaGallery item={{ kind: "post", id: content.id, version: version.id }} className="post-gallery" label="Post media" />}
        </article>
        {detail.decision && !detail.decision.allowed && (
          <aside className="panel purchase-panel">
            <Paywall decision={detail.decision} onCheckout={checkout} />
          </aside>
        )}
        {content.comments !== "hidden" && (
          <section className="panel post-comments">
            <Comments item={{ type: "contents", id: content.id }} />
          </section>
        )}
      </div>
      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this post?</DialogTitle>
            <DialogDescription>The post leaves the channel. It can be restored for 30 days; financial records remain in billing history.</DialogDescription>
          </DialogHeader>
          <FormError>{error}</FormError>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep post
            </Button>
            <Button variant="destructive" disabled={removing} onClick={() => void remove()}>
              {removing && <Spinner data-icon="inline-start" />}
              Delete post
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// A post's editor: its text, access, price, media and publication.
export function EditPostPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const detail = useContentDetail(id);
  const roles = useChannelRoles();
  const owner = detail.content?.channel_id;
  return (
    <div className="page-narrow">
      <div className="page-heading">
        <h1>Edit post</h1>
      </div>
      <ContentEditor
        id={id}
        kind="post"
        contentHref={(c) => postPath({ code: c.code, slug: c.slug ?? "", path: c.path })}
        onDeleted={() => {
          void request(`/api/v1/products/contents/${id}`, {method: "DELETE"})
            .then(() => navigate("/"))
            .catch((cause: unknown) => toast.error("Post deleted; could not retire its price.", {description: cause instanceof Error ? cause.message : "Retry from the pricing section."}));
        }}
      />
      {owner && roles.data?.get(owner) === "owner" && detail.content?.versions.length === 1 && <ProductSettings kind="contents" id={id} version={detail.content.versions[0].id} />}
    </div>
  );
}
