import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { postPage } from "../api";
import { HugeiconsIcon } from "@hugeicons/react";
import { Add01Icon, Image01Icon, UserGroupIcon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { EmptyState, ErrorState, Loading } from "../components/states";
import {
  ChannelCard,
  PostCard,
  SuggestedCreators,
} from "../components/cards";
import { useChannels } from "../channels";
import { useAuth } from "../session";
import { channelsPath, newChannelPath } from "../paths";

function usePosts() {
  const { user } = useAuth();
  const query = useInfiniteQuery({
    queryKey: ["posts", user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      postPage(
        "/api/v1/posts" +
          (pageParam ? "?before=" + encodeURIComponent(pageParam) : ""),
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  return { ...query, data: query.data?.pages.flatMap((page) => page.data) };
}
export function HomePage() {
  const posts = usePosts();
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
            <p>
              Subscribe for members-only posts, unlock individual posts, and
              keep everything you buy.
            </p>
          </div>
          <div className="inline-actions">
            <Button
              size="lg"
              className="bg-white text-primary hover:bg-white/90"
              onClick={auth.openRegister}
            >
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
      {posts.isPending ? (
        <Loading cards />
      ) : posts.error ? (
        <ErrorState error={posts.error} retry={() => void posts.refetch()} />
      ) : posts.data?.length ? (
        <div className="feed">
          {posts.data.map((post) => (
            <PostCard key={post.id} post={post} handle={post.channel_slug} />
          ))}
        </div>
      ) : (
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
      )}
      {posts.hasNextPage && (
        <div className="pagination">
          <Button
            variant="outline"
            disabled={posts.isFetchingNextPage}
            onClick={() => void posts.fetchNextPage()}
          >
            {posts.isFetchingNextPage && <Spinner data-icon="inline-start" />}
            Load more
          </Button>
        </div>
      )}
    </>
  );
}
export function ChannelsPage() {
  const channels = useChannels();
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
      {channels.isPending ? (
        <Loading cards />
      ) : channels.error ? (
        <ErrorState
          error={channels.error}
          retry={() => void channels.refetch()}
        />
      ) : channels.data?.data.length ? (
        <div className="channel-grid">
          {channels.data.data.map((channel) => (
            <ChannelCard channel={channel} key={channel.id} />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={UserGroupIcon}
          title="No channels yet."
          action={
            !auth.user ? (
              <Button onClick={auth.openRegister}>Create an account</Button>
            ) : undefined
          }
        >
          Be the first creator on OnlyDemo.
        </EmptyState>
      )}
      {channels.hasNextPage && (
        <div className="pagination">
          <Button
            variant="outline"
            disabled={channels.isFetchingNextPage}
            onClick={() => void channels.fetchNextPage()}
          >
            {channels.isFetchingNextPage && <Spinner data-icon="inline-start" />}
            More creators
          </Button>
        </div>
      )}
    </>
  );
}
