import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { VideoPoster } from "@openrails/contentkit-upload/ui";
import { cn } from "cn";
import { HugeiconsIcon } from "@hugeicons/react";
import { Bookmark02Icon, FavouriteIcon, Message01Icon, SquareLock02Icon, Tick02Icon } from "@hugeicons/core-free-icons";
import { date } from "../format";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PolicyBadge } from "./policy-badge";
import { Avatar } from "./cards";
import { PostGallery } from "./post-media";
import type { ChannelMembership, Post } from "../models";
import { subscribeLabel, unlockLabel, useJoinFree, usePay } from "./pay";
import { channelPath, postPath } from "../paths";
import { useAuth } from "../session";

// The feed shows this much of a post's text; the post page all of it.
const EXCERPT = 280;
function excerpt(text: string) {
  if (text.length <= EXCERPT) return text;
  const cut = text.slice(0, EXCERPT);
  const space = cut.lastIndexOf(" ");
  return (space > EXCERPT * 0.6 ? cut.slice(0, space) : cut).trimEnd();
}

// Mounts children once the element nears the viewport (feeds read media lazily).
function useNear(eager: boolean) {
  const [el, ref] = useState<HTMLDivElement | null>(null);
  const [near, setNear] = useState(eager || typeof IntersectionObserver !== "function");
  useEffect(() => {
    if (near || !el) return;
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && setNear(true), { rootMargin: "800px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [near, el]);
  return [ref, near] as const;
}

/**
 * One post, identical in every feed and on its page: header, text, all its
 * media (carousel or grid, covers, native aspect, full width), unlock and
 * actions. `variant` changes only the text (the feed shows an excerpt) and
 * where the post page adds editor controls (`controls`, `editor`).
 */
export function PostView({
  post,
  variant = "feed",
  handle,
  membership,
  badges,
  controls,
  editor,
}: {
  post: Post;
  variant?: "feed" | "full";
  handle?: string;
  // Tailors the members-only call to action.
  membership?: ChannelMembership;
  badges?: ReactNode;
  controls?: ReactNode;
  editor?: ReactNode;
}) {
  const full = variant === "full";
  const auth = useAuth();
  const [liked, setLiked] = useState(false);
  const pay = usePay();
  const joinFree = useJoinFree(post.channel_id);
  const unlockRef = useRef<HTMLDivElement>(null);
  const [watchMedia, mediaNear] = useNear(full);
  const policy = post.access_policy;
  const price = post.offers?.find((offer) => !offer.auto_renew);
  const needsMembership = policy === "membership" || (policy === "members_ppv" && !post.has_membership);
  const name = post.channel_name || "Creator";
  const label = post.title || `Post by ${name}`;
  const memberOffer = membership?.status === "open" && !membership.free ? membership.offer : null;
  // Unlocking happens in place: the payment modal opens over the page.
  const unlock = () => {
    if (needsMembership && membership?.free) void joinFree.join();
    else if (needsMembership && memberOffer) pay({ kind: "membership", id: post.channel_id, merchant: name, offer: memberOffer });
    else if (!needsMembership && price && post.offer_status !== "pending") pay({ kind: "post", id: post.id, merchant: name, offer: price });
  };
  const body = post.can_read && post.body ? (full ? post.body : excerpt(post.body)) : "";
  const Title = full ? "h2" : "h3";
  return (
    <article className={cn("feed-card", full && "reader-main")} data-variant={variant}>
      <header className="feed-head">
        <Link to={channelPath(post.channel_slug)} className="feed-author">
          <Avatar name={name} seed={post.channel_id} image={post.channel_avatar} />
          <span>
            <strong>{name}</strong>
            {handle && <small>@{handle}</small>}
          </span>
        </Link>
        <span className="feed-date">
          <PolicyBadge policy={policy} offer={price} /> {badges}
          <Link to={postPath(post)} aria-label={`Open ${label}`}>
            {date(post.created_at)}
          </Link>
        </span>
      </header>
      {(post.title || body || controls) && (
        <div className="feed-text">
          {post.title &&
            (full ? (
              <Title className="reader-title">{post.title}</Title>
            ) : (
              <Title>
                <Link to={postPath(post)}>{post.title}</Link>
              </Title>
            ))}
          {controls}
          {body &&
            (full ? (
              <div className="article-body">{body}</div>
            ) : (
              <p className="feed-body">
                {body}
                {body.length < post.body!.length && (
                  <>
                    {"… "}
                    <Link to={postPath(post)} className="feed-more">
                      more
                    </Link>
                  </>
                )}
              </p>
            ))}
        </div>
      )}
      <div ref={watchMedia}>
        {mediaNear &&
          (!post.can_read && post.poster ? (
            <VideoPoster className="feed-video" poster={post.poster} alt="">
              <span className="feed-video-link" aria-hidden>
                <span className="lock-badge">
                  <HugeiconsIcon icon={SquareLock02Icon} size={30} />
                </span>
              </span>
            </VideoPoster>
          ) : (
            <PostGallery post={post} viewer={auth.user?.id} unlock={() => unlockRef.current?.scrollIntoView({ behavior: "smooth", block: "center" })} />
          ))}
      </div>
      {editor}
      {!post.can_read && !(needsMembership && membership?.status === "closed") && (
        <div className="feed-unlock" ref={unlockRef}>
          {needsMembership && !membership?.free && !memberOffer ? (
            <Button size="lg" className="w-full" nativeButton={false} render={<Link to={channelPath(post.channel_slug)} />}>
              <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
              Subscribe to unlock
            </Button>
          ) : (
            <Button
              size="lg"
              className="w-full"
              disabled={joinFree.pending || (!needsMembership && (!price || post.offer_status === "pending"))}
              onClick={unlock}
            >
              <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
              {needsMembership
                ? membership?.free
                  ? "Join free to unlock"
                  : subscribeLabel(memberOffer!)
                : post.offer_status === "pending"
                  ? "Price pending"
                  : price
                    ? unlockLabel(price)
                    : "Unlock post"}
            </Button>
          )}
        </div>
      )}
      <footer className="feed-actions">
        <Button variant="ghost" size="icon" className={cn(liked && "liked")} aria-label="Like" aria-pressed={liked} onClick={() => setLiked(!liked)}>
          <HugeiconsIcon icon={FavouriteIcon} size={21} />
        </Button>
        {!full && (
          <Button variant="ghost" size="icon" aria-label="Open post" nativeButton={false} render={<Link to={postPath(post)} />}>
            <HugeiconsIcon icon={Message01Icon} size={21} />
          </Button>
        )}
        <span className="feed-actions-end">
          {post.purchased ? (
            <Badge className="bg-success/10 text-success">
              <HugeiconsIcon icon={Tick02Icon} data-icon="inline-start" />
              Purchased
            </Badge>
          ) : (
            policy === "public" && <Badge variant="secondary">Free</Badge>
          )}
          <HugeiconsIcon icon={Bookmark02Icon} size={20} className="muted" />
        </span>
      </footer>
    </article>
  );
}
