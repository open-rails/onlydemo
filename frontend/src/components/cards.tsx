import { Link } from "react-router-dom";
import type { CSSProperties, ReactNode } from "react";
import type { PublicPreset } from "@openrails/contentkit-ui/client";
import { RenditionImg } from "@openrails/contentkit-ui";
import { usePublicImage } from "@openrails/contentkit-ui/react";
import { cn } from "cn";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type Channel } from "../models";
import { hue, membershipOffer, useChannels } from "../channels";
import { subscribeLabel, useJoinFree, usePay } from "./pay";
import { channelsPath, channelPath } from "../paths";

const tint = (seed: string | number) =>
  ({ "--h": hue(seed) }) as CSSProperties;

// The box, not the preset's aspect, sizes the image (object-fit: cover).
const fill: CSSProperties = { position: "absolute", inset: 0, width: "100%", height: "100%", objectFit: "cover", borderRadius: "inherit" };

// A public image over its generated placeholder, at the width its box needs.
const PublicImg = ({ image }: { image?: PublicPreset | null }) => <RenditionImg outputs={image?.renditions} style={fill} />;

export function Avatar({
  name,
  seed,
  image,
  className,
}: {
  name: string;
  seed: string | number;
  image?: PublicPreset | null;
  sizes?: string;
  className?: string;
}) {
  return (
    <span className={cn("avatar", className)} style={tint(seed)}>
      {(name || "?").slice(0, 1).toUpperCase()}
      <PublicImg image={image} />
    </span>
  );
}
// The signed-in user's avatar: their item's published avatar, if any.
export function UserAvatar({ user, className }: { user: { id: string; username: string }; className?: string }) {
  const avatar = usePublicImage("user", user.id, "avatar");
  return <Avatar name={user.username} seed={user.id} image={avatar.isDefault ? null : avatar.image} className={className} />;
}
export function Cover({
  seed,
  image,
  className,
  children,
}: {
  seed: string | number;
  image?: PublicPreset | null;
  sizes?: string;
  className?: string;
  children?: ReactNode;
}) {
  return (
    <div className={cn("cover", className)} style={tint(seed)}>
      <PublicImg image={image} />
      {children}
    </div>
  );
}
export function ChannelCard({ channel }: { channel: Channel }) {
  const offer = membershipOffer(channel);
  const pay = usePay();
  const joinFree = useJoinFree(channel.id);
  const joinable = !channel.membership.member && !channel.can_manage && !channel.can_edit;
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
          {joinable && offer ? (
            <Button
              size="sm"
              onClick={() => pay({ kind: "membership", id: channel.id, merchant: channel.name, offer })}
            >
              {subscribeLabel(offer)}
            </Button>
          ) : joinable && channel.membership.status === "open" && channel.membership.free ? (
            <Button size="sm" disabled={joinFree.pending} onClick={() => void joinFree.join()}>
              Join free
            </Button>
          ) : (
            <Button
              size="sm"
              nativeButton={false}
              render={<Link to={channelPath(channel.slug)} />}
            >
              View profile
            </Button>
          )}
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
