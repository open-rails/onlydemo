import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { ContentEditor, GroupAvatar, MediaGallery, Paywall } from "@openrails/contentkit-ui";
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
import { dateTime } from "../format";
import { channelPath, editPostPath, parseCode, postLink, postPath } from "../paths";

// A post's content id, from its code (/p/{CODE}/{slug}) or its id (/post/{id}).
function usePostID() {
  const params = useParams();
  const code = params.id ? null : parseCode(params.code ?? "");
  const resolved = useQuery({
    queryKey: ["code", code],
    queryFn: () => contentkit.codes.resolve(code!),
    enabled: !!code,
  });
  return { id: params.id ?? resolved.data?.content_id ?? null, missing: !params.id && (!code || !!resolved.error), loading: resolved.isPending && !!code };
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
  const owner = content?.owner;
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
  if (post.loading || detail.loading) return <Loading />;
  if (detail.error?.status === 404) return <NotFoundPage />;
  if (detail.error || !content) return <ErrorState error={detail.error ?? new Error("Post not found.")} retry={detail.reload} />;
  const remove = async () => {
    setRemoving(true);
    setError("");
    try {
      await contentkit.contents.remove(content.id);
      await client.invalidateQueries();
      navigate(owner ? channelPath(owner.slug) : "/");
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
        <Button variant="ghost" size="icon" aria-label="Back to creator" nativeButton={false} render={<Link to={owner ? channelPath(owner.slug) : "/"} />}>
          <HugeiconsIcon icon={ArrowLeft02Icon} />
        </Button>
        <h1>Post</h1>
      </div>
      <div className="reader-layout">
        <article className="feed-card reader-main" data-variant="full">
          <header className="feed-head">
            {owner && (
              <Link to={channelPath(owner.slug)} className="feed-author">
                <GroupAvatar group={owner} className="avatar" />
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
          <div className="feed-text">
            {content.title && <h2 className="reader-title">{content.title}</h2>}
            {/* The body is the viewer's once they open the post; the description is its teaser. */}
            <div className="article-body">{content.body || content.description}</div>
          </div>
          {version && <MediaGallery item={{ kind: "post", id: content.id, version: version.id }} className="post-gallery" label="Post media" />}
        </article>
        <aside className="panel purchase-panel">{detail.decision && <Paywall decision={detail.decision} onCheckout={checkout} />}</aside>
      </div>
      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this post?</DialogTitle>
            <DialogDescription>The post leaves the channel. Its buyers keep it; financial records remain in billing history.</DialogDescription>
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
  return (
    <div className="page-narrow">
      <div className="page-heading">
        <h1>Edit post</h1>
      </div>
      <ContentEditor
        id={id}
        kind="post"
        contentHref={(c) => postPath({ code: c.code, slug: c.slug ?? "", path: c.path })}
        onDeleted={() => navigate("/")}
      />
    </div>
  );
}
