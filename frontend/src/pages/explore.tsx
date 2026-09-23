import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { postPage } from "../api";
import {
  Button,
  EmptyState,
  ErrorState,
  Icon,
  Loading,
} from "../components/ui";
import {
  ChannelCard,
  PostCard,
  SuggestedCreators,
} from "../components/cards";
import { useChannels } from "../channels";
import { useAuth } from "../auth-context";

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
            <Button onClick={auth.openRegister}>Sign up</Button>
            <Button variant="secondary" onClick={auth.openLogin}>
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
          icon="image"
          title="Your feed is empty."
          action={
            <Link to="/channels" className="button button-primary">
              Find creators
            </Link>
          }
        >
          New posts appear here as creators publish them.
        </EmptyState>
      )}
      {posts.hasNextPage && (
        <div className="pagination">
          <Button
            variant="secondary"
            busy={posts.isFetchingNextPage}
            onClick={() => void posts.fetchNextPage()}
          >
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
          <Link className="button button-primary button-sm" to="/channels/new">
            <Icon name="plus" size={16} />
            New channel
          </Link>
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
          icon="users"
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
            variant="secondary"
            busy={channels.isFetchingNextPage}
            onClick={() => void channels.fetchNextPage()}
          >
            More creators
          </Button>
        </div>
      )}
    </>
  );
}
