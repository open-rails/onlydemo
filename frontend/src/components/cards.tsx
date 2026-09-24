import { Link } from "react-router-dom";
import { type CSSProperties, type ReactNode } from "react";
import type { SlotManifest } from "@openrails/contentkit-upload";
import { SlotImage } from "@openrails/contentkit-upload/ui";
import { cn } from "cn";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type Channel } from "../models";
import { hue, membershipOffer, useChannels } from "../channels";
import { subscribeLabel, useJoinFree, usePay } from "./pay";
import { channelsPath, channelPath } from "../paths";
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
      {hasImage(image) && <SlotImage manifest={image} round style={fill} />}
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
      {hasImage(image) && <SlotImage manifest={image} style={fill} />}
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
