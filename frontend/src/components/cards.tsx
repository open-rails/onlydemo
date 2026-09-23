import { Link } from "react-router-dom";
import type { CSSProperties } from "react";
import { money, date, duration } from "../format";
import { Badge, Icon } from "./ui";
import { policyLabels, type Channel, type Post } from "../models";

const covers = [
  "#825d70",
  "#5c7470",
  "#74634c",
  "#5d6984",
  "#806943",
  "#6b6283",
];
export function PostCard({ post }: { post: Post }) {
  const policy = post.access_policy;
  const price = post.offers?.find((offer) => !offer.auto_renew);
  return (
    <article className="post-card">
      <Link
        to={`/posts/${post.id}`}
        className="post-cover"
        style={{ "--cover": covers[post.id % covers.length] } as CSSProperties}
        tabIndex={-1}
        aria-hidden="true"
      >
        <span className="cover-title">{post.title}</span>
        {!post.can_read && (
          <span className="cover-lock">
            <Icon name="lock" size={15} />
          </span>
        )}
      </Link>
      <div className="card-body">
        <Link to={`/channels/${post.channel_id}`} className="card-channel">
          <span className="avatar">
            {(post.channel_name || "C").slice(0, 1)}
          </span>
          {post.channel_name || "View channel"}
        </Link>
        <h3>
          <Link to={`/posts/${post.id}`}>{post.title}</Link>
        </h3>
        {post.body && <p className="card-excerpt">{post.body}</p>}
        <div>
          <Badge
            tone={
              post.purchased
                ? "success"
                : policy === "public"
                  ? "neutral"
                  : "brand"
            }
          >
            {post.purchased ? (
              <>
                <Icon name="check" size={11} />
                Purchased
              </>
            ) : (
              policyLabels[policy]
            )}
          </Badge>
        </div>
        <div className="card-bottom">
          <span>{date(post.created_at)}</span>
          <span>
            {price
              ? money(price.unit_amount, price.currency)
              : post.can_read
                ? "Read story"
                : "View options"}{" "}
            <span aria-hidden="true">↗</span>
          </span>
        </div>
      </div>
    </article>
  );
}
export function ChannelCard({ channel }: { channel: Channel }) {
  return (
    <article className="channel-card">
      <div className="channel-avatar">
        {channel.name.slice(0, 1).toUpperCase()}
      </div>
      <div>
        <h3>
          <Link to={`/channels/${channel.id}`}>{channel.name}</Link>
        </h3>
        <p>@{channel.slug}</p>
        {channel.offers?.find((o) => o.auto_renew) && (
          <p>
            {money(
              channel.offers.find((o) => o.auto_renew)!.unit_amount,
              channel.offers.find((o) => o.auto_renew)!.currency,
            )}{" "}
            ·{" "}
            {duration(
              channel.offers.find((o) => o.auto_renew)!.access_duration_hours,
            )}
          </p>
        )}
      </div>
      {channel.description && <p>{channel.description}</p>}
      <div className="channel-card-footer">
        <span>
          {channel.can_manage || channel.can_edit ? (
            <Badge tone="brand">
              {channel.can_manage ? "Owner" : "Editor"}
            </Badge>
          ) : channel.has_membership ? (
            <Badge tone="success">Subscribed</Badge>
          ) : channel.post_count != null ? (
            `${channel.post_count} ${channel.post_count === 1 ? "post" : "posts"}`
          ) : (
            "Independent channel"
          )}
        </span>
        <Link to={`/channels/${channel.id}`} className="text-button">
          Visit channel <span aria-hidden="true">↗</span>
        </Link>
      </div>
    </article>
  );
}
