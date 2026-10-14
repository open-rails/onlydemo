import { Link } from "react-router-dom";
import type { Card } from "@openrails/contentkit-ui/client";
import { ContentGrid, GroupAvatar, GroupDirectory, PostCard } from "@openrails/contentkit-ui";
import { HugeiconsIcon } from "@hugeicons/react";
import { Add01Icon, Image01Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { EmptyState } from "../components/states";
import { SuggestedCreators } from "../components/cards";
import { SiteSearch } from "./search";
import { useAuth } from "../session";
import { channelPath, channelsPath, newChannelPath } from "../paths";

// A post card with its channel above it; owner={false} on that channel's own feed.
export function Post({ card, owner: showOwner = true }: { card: Card; owner?: boolean }) {
  const owner = showOwner ? card.owner : undefined;
  return (
    <PostCard
      card={card}
      owner={showOwner}
      byline={
        owner && (
          <Link to={channelPath(owner)} className="feed-author">
            <GroupAvatar group={owner} className="avatar" />
            <span>
              <strong>{owner.name}</strong>
              <small>@{owner.slug}</small>
            </span>
          </Link>
        )
      }
    />
  );
}

export function HomePage() {
  const auth = useAuth();
  return (
    <>
      <div className="page-title">
        <h1>Home</h1>
      </div>
      {!auth.user && (
        <section className="welcome-card">
          <div>
            <h2>Support the creators you love.</h2>
            <p>Subscribe for members-only posts, unlock individual posts, and keep everything you buy.</p>
          </div>
          <div className="inline-actions">
            <Button size="lg" className="bg-white text-primary hover:bg-white/90" onClick={auth.openRegister}>
              Sign up
            </Button>
            <Button
              size="lg"
              variant="outline"
              className="border-white/70 bg-transparent text-white hover:bg-white/15 hover:text-white dark:bg-transparent"
              onClick={auth.openLogin}
            >
              Log in
            </Button>
          </div>
        </section>
      )}
      <SuggestedCreators strip />
      <ContentGrid
        className="feed"
        surface="home"
        query={{ kind: "post", sort: "newest", mode: "lock" }}
        renderCard={(card) => <Post card={card} />}
        empty={
          <EmptyState
            icon={Image01Icon}
            title="Your feed is empty."
            action={
              <Button nativeButton={false} render={<Link to={channelsPath} />}>
                Find creators
              </Button>
            }
          >
            New posts appear here as creators publish them.
          </EmptyState>
        }
      />
    </>
  );
}

export function ChannelsPage() {
  const auth = useAuth();
  return (
    <>
      <div className="page-title">
        <h1>Explore creators</h1>
        {auth.user && (
          <Button size="sm" nativeButton={false} render={<Link to={newChannelPath} />}>
            <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
            New channel
          </Button>
        )}
      </div>
      <SiteSearch className="mb-4" />
      <GroupDirectory kind="channel" layout="grid" sort="newest" letters={false} href={(g) => channelPath(g)} />
    </>
  );
}
