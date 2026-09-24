import { Link } from "react-router-dom";
import { useState, type CSSProperties, type ReactNode } from "react";
import type { SlotManifest } from "@openrails/contentkit-upload";
import { SlotImage } from "@openrails/contentkit-upload/ui";
import { cn } from "cn";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Bookmark02Icon,
  FavouriteIcon,
  Image01Icon,
  Message01Icon,
  SquareLock02Icon,
  Tick02Icon,
} from "@hugeicons/core-free-icons";
import { money, date, duration } from "../format";
import { Badge } from "@/components/ui/badge";
import { PolicyBadge } from "./policy-badge";
import { Button } from "@/components/ui/button";
import {
  policyLabels,
  type Channel,
  type ChannelMembership,
  type Post,
} from "../models";
import { hue, membershipOffer, useChannels } from "../channels";
import { channelsPath, channelPath, postPath } from "../paths";
import { userRef, useSlot } from "../media";

const tint = (seed: string | number) =>
  ({ "--h": hue(seed) }) as CSSProperties;

// Slot images (srcset at every rendered width; sizes is the CSS box) over a
// generated placeholder shown while unset.
const hasImage = (m?: SlotManifest | null): m is SlotManifest => !!m?.outputs.length;
// The box, not the slot aspect, sizes the image (object-fit: cover).
const fill: CSSProperties = { position: "absolute", inset: 0, width: "100%", height: "100%", aspectRatio: "auto", borderRadius: "inherit" };
export function Avatar({
  name,
  seed,
  image,
  sizes = "40px",
  className,
}: {
  name: string;
  seed: string | number;
  image?: SlotManifest | null;
  sizes?: string;
  className?: string;
}) {
  return (
    <span className={cn("avatar", className)} style={tint(seed)}>
      {(name || "?").slice(0, 1).toUpperCase()}
      {hasImage(image) && <SlotImage manifest={image} sizes={sizes} round style={fill} />}
    </span>
  );
}
// The signed-in user's avatar, shared with the account page's editor.
export function UserAvatar({ user, className }: { user: { id: string; username: string }; className?: string }) {
  const image = useSlot(userRef(user.id), "avatar");
  return <Avatar name={user.username} seed={user.id} image={image} className={className} />;
}
export function Cover({
  seed,
  image,
  sizes = "(min-width: 768px) 360px, 100vw",
  className,
  children,
}: {
  seed: string | number;
  image?: SlotManifest | null;
  sizes?: string;
  className?: string;
  children?: ReactNode;
}) {
  return (
    <div className={cn("cover", className)} style={tint(seed)}>
      {hasImage(image) && <SlotImage manifest={image} sizes={sizes} style={fill} />}
      {children}
    </div>
  );
}
// membership, when known, tailors the members-only call to action.
export function PostCard({
  post,
  handle,
  membership,
}: {
  post: Post;
  handle?: string;
  membership?: ChannelMembership;
}) {
  const [liked, setLiked] = useState(false);
  const policy = post.access_policy;
  const price = post.offers?.find((offer) => !offer.auto_renew);
  const needsMembership =
    policy === "membership" ||
    (policy === "members_ppv" && !post.has_membership);
  const name = post.channel_name || "Creator";
  return (
    <article className="feed-card">
      <header className="feed-head">
        <Link to={channelPath(post.channel_slug)} className="feed-author">
          <Avatar name={name} seed={post.channel_id} image={post.channel_avatar} />
          <span>
            <strong>
              {name}
            </strong>
            {handle && <small>@{handle}</small>}
          </span>
        </Link>
        <span className="feed-date">
          <PolicyBadge policy={policy} offer={price} />{" "}
          {date(post.created_at)}
        </span>
      </header>
      <div className="feed-text">
        <h3>
          <Link to={postPath(post)}>{post.title}</Link>
        </h3>
        {post.can_read && post.body && <p>{post.body}</p>}
      </div>
      {!post.can_read && (
        <Link
          to={postPath(post)}
          className="feed-media"
          style={tint(post.channel_id)}
          aria-label={`Unlock ${post.title}`}
        >
          <span className="lock-badge">
            <HugeiconsIcon icon={SquareLock02Icon} size={30} />
          </span>
          <span className="media-meta">
            <HugeiconsIcon icon={Image01Icon} size={15} /> {policyLabels[policy]}
          </span>
        </Link>
      )}
      {!post.can_read && !(needsMembership && membership?.status === "closed") && (
        <div className="feed-unlock">
          <Button
            size="lg"
            className="w-full"
            nativeButton={false}
            render={
              <Link
                to={
                  needsMembership
                    ? channelPath(post.channel_slug)
                    : postPath(post)
                }
              />
            }
          >
            <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
            {needsMembership
              ? membership?.free
                ? "Join free to unlock"
                : "Subscribe to unlock"
              : post.offer_status === "pending"
                ? "Price pending"
                : price
                ? `Unlock for ${money(price.unit_amount, price.currency)}`
                : "Unlock post"}
          </Button>
        </div>
      )}
      <footer className="feed-actions">
        <Button
          variant="ghost"
          size="icon"
          className={cn(liked && "liked")}
          aria-label="Like"
          aria-pressed={liked}
          onClick={() => setLiked(!liked)}
        >
          <HugeiconsIcon icon={FavouriteIcon} size={21} />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          aria-label="Open post"
          nativeButton={false}
          render={<Link to={postPath(post)} />}
        >
          <HugeiconsIcon icon={Message01Icon} size={21} />
        </Button>
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
export function ChannelCard({ channel }: { channel: Channel }) {
  const offer = membershipOffer(channel);
  return (
    <article className="creator-card">
      <Cover seed={channel.id} image={channel.cover} />
      <div className="creator-card-body">
        <Avatar name={channel.name} seed={channel.id} image={channel.avatar} sizes="72px" className="avatar-lg" />
        <h3>
          <Link to={channelPath(channel.slug)}>{channel.name}</Link>
        </h3>
        <p className="handle">@{channel.slug}</p>
        {channel.description && (
          <p className="creator-bio">{channel.description}</p>
        )}
        <div className="creator-card-footer">
          {channel.can_manage || channel.can_edit ? (
            <Badge>{channel.can_manage ? "Owner" : "Editor"}</Badge>
          ) : channel.membership.member ? (
            <Badge className="bg-success/10 text-success">Member</Badge>
          ) : (
            channel.post_count != null && (
              <span className="muted">
                {channel.post_count}{" "}
                {channel.post_count === 1 ? "post" : "posts"}
              </span>
            )
          )}
          <Button
            size="sm"
            nativeButton={false}
            render={<Link to={channelPath(channel.slug)} />}
          >
            {channel.membership.member
              ? "View profile"
              : offer
                ? `${money(offer.unit_amount, offer.currency)} ${duration(offer.access_duration_hours).replace("every ", "/ ")}`
                : channel.membership.status === "open" && channel.membership.free
                  ? "Join free"
                  : "View profile"}
          </Button>
        </div>
      </div>
    </article>
  );
}
export function SuggestedCreators({ strip = false }: { strip?: boolean }) {
  const channels = useChannels();
  const list = channels.data?.data.slice(0, strip ? 10 : 5) || [];
  if (!list.length) return null;
  return (
    <section className={cn("suggested", strip && "suggested-strip")}>
      <div className="suggested-heading">
        <h2>Suggestions</h2>
        <Button variant="link" size="sm" nativeButton={false} render={<Link to={channelsPath} />}>
          See all
        </Button>
      </div>
      <div className="suggested-list">
        {list.map((channel) => (
          <Link
            to={channelPath(channel.slug)}
            key={channel.id}
            className="suggested-item"
          >
            <Cover seed={channel.id} image={channel.cover} sizes="260px" />
            <span className="suggested-info">
              <Avatar name={channel.name} seed={channel.id} image={channel.avatar} sizes="52px" />
              <span>
                <strong>{channel.name}</strong>
                <small>@{channel.slug}</small>
              </span>
            </span>
          </Link>
        ))}
      </div>
    </section>
  );
}
