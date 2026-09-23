import { Link } from "react-router-dom";
import { useState, type CSSProperties, type ReactNode } from "react";
import { clsx } from "clsx";
import { money, date, duration } from "../format";
import { Badge, Icon } from "./ui";
import { policyLabels, type Channel, type Post } from "../models";
import { hue, membershipOffer, useChannels } from "../channels";

const tint = (seed: string | number) =>
  ({ "--h": hue(seed) }) as CSSProperties;

export function Avatar({
  name,
  seed,
  className,
}: {
  name: string;
  seed: string | number;
  className?: string;
}) {
  return (
    <span className={clsx("avatar", className)} style={tint(seed)}>
      {(name || "?").slice(0, 1).toUpperCase()}
    </span>
  );
}
export function Cover({
  seed,
  className,
  children,
}: {
  seed: string | number;
  className?: string;
  children?: ReactNode;
}) {
  return (
    <div className={clsx("cover", className)} style={tint(seed)}>
      {children}
    </div>
  );
}
export function PostCard({ post, handle }: { post: Post; handle?: string }) {
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
        <Link to={`/channels/${post.channel_slug}`} className="feed-author">
          <Avatar name={name} seed={post.channel_id} />
          <span>
            <strong>
              {name}
            </strong>
            {handle && <small>@{handle}</small>}
          </span>
        </Link>
        <span className="feed-date">{date(post.created_at)}</span>
      </header>
      <div className="feed-text">
        <h3>
          <Link to={`/posts/${post.id}`}>{post.title}</Link>
        </h3>
        {post.can_read && post.body && <p>{post.body}</p>}
      </div>
      {!post.can_read && (
        <Link
          to={`/posts/${post.id}`}
          className="feed-media"
          style={tint(post.channel_id)}
          aria-label={`Unlock ${post.title}`}
        >
          <span className="lock-badge">
            <Icon name="lock" size={30} />
          </span>
          <span className="media-meta">
            <Icon name="image" size={15} /> {policyLabels[policy]}
          </span>
        </Link>
      )}
      {!post.can_read && (
        <div className="feed-unlock">
          <Link
            to={
              needsMembership
                ? `/channels/${post.channel_slug}`
                : `/posts/${post.id}`
            }
            className="button button-primary button-full"
          >
            <Icon name="lock" size={16} />
            {needsMembership
              ? "Subscribe to unlock"
              : post.offer_status === "pending"
                ? "Price pending"
                : price
                ? `Unlock for ${money(price.unit_amount, price.currency)}`
                : "Unlock post"}
          </Link>
        </div>
      )}
      <footer className="feed-actions">
        <button
          className={clsx("icon-button", liked && "liked")}
          aria-label="Like"
          aria-pressed={liked}
          onClick={() => setLiked(!liked)}
        >
          <Icon name="heart" size={21} />
        </button>
        <Link
          to={`/posts/${post.id}`}
          className="icon-button"
          aria-label="Open post"
        >
          <Icon name="message" size={21} />
        </Link>
        <Link to={`/channels/${post.channel_slug}`} className="tip-link">
          <Icon name="dollar" size={21} />
          Send tip
        </Link>
        <span className="feed-actions-end">
          {post.purchased ? (
            <Badge tone="success">
              <Icon name="check" size={11} />
              Purchased
            </Badge>
          ) : (
            policy === "public" && <Badge>Free</Badge>
          )}
          <Icon name="bookmark" size={20} className="muted" />
        </span>
      </footer>
    </article>
  );
}
export function ChannelCard({ channel }: { channel: Channel }) {
  const offer = membershipOffer(channel);
  return (
    <article className="creator-card">
      <Cover seed={channel.id} />
      <div className="creator-card-body">
        <Avatar name={channel.name} seed={channel.id} className="avatar-lg" />
        <h3>
          <Link to={`/channels/${channel.slug}`}>{channel.name}</Link>
        </h3>
        <p className="handle">@{channel.slug}</p>
        {channel.description && (
          <p className="creator-bio">{channel.description}</p>
        )}
        <div className="creator-card-footer">
          {channel.can_manage || channel.can_edit ? (
            <Badge tone="brand">{channel.can_manage ? "Owner" : "Editor"}</Badge>
          ) : channel.has_membership ? (
            <Badge tone="success">Subscribed</Badge>
          ) : (
            channel.post_count != null && (
              <span className="muted">
                {channel.post_count}{" "}
                {channel.post_count === 1 ? "post" : "posts"}
              </span>
            )
          )}
          <Link
            to={`/channels/${channel.slug}`}
            className="button button-primary button-sm"
          >
            {offer && !channel.has_membership
              ? `${money(offer.unit_amount, offer.currency)} ${duration(offer.access_duration_hours).replace("every ", "/ ")}`
              : "View profile"}
          </Link>
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
    <section className={clsx("suggested", strip && "suggested-strip")}>
      <div className="suggested-heading">
        <h2>Suggestions</h2>
        <Link to="/channels" className="text-button">
          See all
        </Link>
      </div>
      <div className="suggested-list">
        {list.map((channel) => (
          <Link
            to={`/channels/${channel.slug}`}
            key={channel.id}
            className="suggested-item"
          >
            <Cover seed={channel.id} />
            <span className="suggested-info">
              <Avatar name={channel.name} seed={channel.id} />
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
