import { Link } from "react-router-dom";
import type { CSSProperties } from "react";
import { RenditionImg } from "@openrails/contentkit-ui";
import { ChannelAvatar, ChannelCover } from "@openrails/contentkit-ui";
import { usePublicImage, useChannels } from "@openrails/contentkit-ui/react";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import { channelsPath, channelPath } from "../paths";
import { userRef } from "../media";

function hue(seed: string) {
  let h = 0;
  for (const c of seed) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}

// The signed-in user's avatar: their item's published avatar, else an initial.
export function UserAvatar({ user, className }: { user: { id: string; username: string }; className?: string }) {
  const avatar = usePublicImage(userRef(user.id), "avatar");
  return (
    <span className={cn("avatar", className)} style={{ "--h": hue(user.id) } as CSSProperties}>
      {(user.username || "?").slice(0, 1).toUpperCase()}
      {!avatar.isDefault && (
        <RenditionImg
          outputs={avatar.image?.renditions}
          style={{ position: "absolute", inset: 0, width: "100%", height: "100%", objectFit: "cover", borderRadius: "inherit" }}
        />
      )}
    </span>
  );
}

// Newest channels, from ContentKit.
export function SuggestedCreators({ strip = false }: { strip?: boolean }) {
  const channels = useChannels({ sort: "newest", limit: strip ? 10 : 5 });
  if (!channels.items.length) return null;
  return (
    <section className={cn("suggested", strip && "suggested-strip")}>
      <div className="suggested-heading">
        <h2>Suggestions</h2>
        <Button variant="link" size="sm" nativeButton={false} render={<Link to={channelsPath} />}>
          See all
        </Button>
      </div>
      <div className="suggested-list">
        {channels.items.map((channel) => (
          <Link to={channelPath(channel)} key={channel.id} className="suggested-item">
            <ChannelCover channel={channel} className="cover" />
            <span className="suggested-info">
              <ChannelAvatar channel={channel} className="avatar" />
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
