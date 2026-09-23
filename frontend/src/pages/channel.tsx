import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { request, postPage } from "../api";
import { useAuth } from "../auth-context";
import type { Channel } from "../models";
import { micros, money, duration } from "../format";
import {
  Button,
  EmptyState,
  ErrorState,
  Field,
  Icon,
  Loading,
  Modal,
} from "../components/ui";
import { Avatar, Cover, PostCard } from "../components/cards";
import { membershipOffer } from "../channels";
import { PostEditor } from "../components/post-editor";
import { ChannelTeam } from "../components/channel-team";
import { MembershipDialog } from "../components/membership";

export function ChannelPage() {
  const { id = "" } = useParams();
  const auth = useAuth();
  const client = useQueryClient();
  const [editor, setEditor] = useState(false);
  const [membership, setMembership] = useState(false);
  const [tab, setTab] = useState("posts");
  const channel = useQuery({
    queryKey: ["channel", id, auth.user?.id],
    queryFn: () => request<Channel>(`/api/v1/channels/${id}`),
  });
  const postQuery = useInfiniteQuery({
    queryKey: ["channel-posts", id, auth.user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      postPage(
        `/api/v1/posts?channel_id=${encodeURIComponent(id)}${pageParam ? "&before=" + encodeURIComponent(pageParam) : ""}`,
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const posts = {
    ...postQuery,
    data: postQuery.data?.pages.flatMap((page) => page.data),
  };
  if (channel.isPending) return <Loading />;
  if (channel.error || !channel.data)
    return (
      <ErrorState
        error={channel.error || new Error("Channel not found.")}
        retry={() => void channel.refetch()}
      />
    );
  const current = channel.data;
  const offer = membershipOffer(current);
  const postCount = current.post_count ?? posts.data?.length ?? 0;
  return (
    <>
      <section className="profile">
        <Cover seed={current.id} className="profile-cover">
          <Link to="/channels" className="cover-back" aria-label="Back">
            <Icon name="arrow" size={20} />
          </Link>
          <div className="cover-title">
            <strong>{current.name}</strong>
            <small>
              {postCount} {postCount === 1 ? "post" : "posts"}
            </small>
          </div>
        </Cover>
        <div className="profile-body">
          <div className="profile-top">
            <Avatar
              name={current.name}
              seed={current.id}
              className="avatar-xl"
            />
            <div className="inline-actions">
              {current.can_edit && (
                <Button onClick={() => setEditor(true)}>
                  <Icon name="plus" size={16} />
                  New post
                </Button>
              )}
              <button
                className="icon-button icon-button-outline"
                aria-label="Tip creator"
              >
                <Icon name="dollar" size={20} />
              </button>
              <button
                className="icon-button icon-button-outline"
                aria-label="Favorite creator"
              >
                <Icon name="star" size={20} />
              </button>
            </div>
          </div>
          <h1 className="profile-name">
            {current.name}
          </h1>
          <p className="handle">
            @{current.slug} · <span className="online">Available now</span>
          </p>
          {current.description && (
            <p className="profile-bio">{current.description}</p>
          )}
          {!current.can_edit && (
            <div className="subscribe-box">
              <span className="subscribe-label">Subscription</span>
              {current.has_membership ? (
                <div className="button button-subscribed button-full">
                  <Icon name="check" size={18} />
                  Subscribed
                </div>
              ) : offer ? (
                <button
                  className="button button-primary button-full button-subscribe"
                  onClick={() => setMembership(true)}
                >
                  <span>Subscribe</span>
                  <span>
                    {money(offer.unit_amount, offer.currency)}{" "}
                    {duration(offer.access_duration_hours).replace("every ", "/ ")}
                  </span>
                </button>
              ) : (
                <p className="muted">This creator has no subscription yet.</p>
              )}
            </div>
          )}
        </div>
      </section>
      <div className="tabs" aria-label="Channel sections">
        <button
          className={`tab ${tab === "posts" ? "active" : ""}`}
          onClick={() => setTab("posts")}
        >
          {postCount} {postCount === 1 ? "Post" : "Posts"}
        </button>
        <button
          className={`tab ${tab === "about" ? "active" : ""}`}
          onClick={() => setTab("about")}
        >
          About
        </button>
        {current.can_edit && (
          <button
            className={`tab ${tab === "team" ? "active" : ""}`}
            onClick={() => setTab("team")}
          >
            Team
          </button>
        )}
        {current.can_manage && (
          <button
            className={`tab ${tab === "settings" ? "active" : ""}`}
            onClick={() => setTab("settings")}
          >
            Settings
          </button>
        )}
      </div>
      {tab === "posts" &&
        (posts.isPending ? (
          <Loading cards />
        ) : posts.error ? (
          <ErrorState error={posts.error} retry={() => void posts.refetch()} />
        ) : posts.data?.length ? (
          <div className="feed">
            {posts.data.map((post) => (
              <PostCard
                post={{ ...post, channel_name: current.name }}
                handle={current.slug}
                key={post.id}
              />
            ))}
          </div>
        ) : (
          <EmptyState
            title="No posts yet."
            action={
              current.can_edit ? (
                <Button onClick={() => setEditor(true)}>
                  Write the first post
                </Button>
              ) : undefined
            }
          >
            Check back soon for the first post from this creator.
          </EmptyState>
        ))}
      {tab === "posts" && posts.hasNextPage && (
        <div className="pagination">
          <Button
            variant="secondary"
            busy={posts.isFetchingNextPage}
            onClick={() => void posts.fetchNextPage()}
          >
            Older posts
          </Button>
        </div>
      )}
      {tab === "about" && (
        <div className="panel stack">
          <h2>About {current.name}</h2>
          <p>
            {current.description ||
              "A creator on OnlyDemo."}
          </p>
          <p className="muted">
            Membership includes posts marked “Included with membership” while
            subscribed. Some posts are sold separately. Every purchased post
            keeps permanent access, even after membership ends.
          </p>
          <p className="muted">
            Channel owners and editors manage publication. Paying readers do not
            receive editing permissions.
          </p>
        </div>
      )}
      {tab === "team" && current.can_edit && <ChannelTeam channel={current} />}
      {tab === "settings" && current.can_manage && (
        <ChannelSettings
          channel={current}
          onUpdated={() =>
            void client.invalidateQueries({ queryKey: ["channel", id] })
          }
        />
      )}
      <PostEditor
        open={editor}
        channelID={id}
        onClose={() => setEditor(false)}
      />
      <MembershipDialog
        open={membership}
        channel={current}
        onClose={() => setMembership(false)}
      />
    </>
  );
}
function ChannelSettings({
  channel,
  onUpdated,
}: {
  channel: Channel;
  onUpdated: () => void;
}) {
  const navigate = useNavigate();
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const offer = channel.offers.find((item) => item.auto_renew);
  const save = useMutation({
    mutationFn: (unit_amount: string) =>
      request(`/api/v1/channels/${channel.id}/membership`, {
        method: "PUT",
        body: JSON.stringify({ unit_amount, currency: "USD" }),
      }),
    onSuccess: onUpdated,
  });
  const remove = useMutation({
    mutationFn: () =>
      request(`/api/v1/channels/${channel.id}`, { method: "DELETE" }),
    onSuccess: () => navigate("/me?tab=channels"),
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    try {
      save.mutate(
        micros(String(new FormData(event.currentTarget).get("amount"))),
      );
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Invalid price.");
    }
  };
  return (
    <div className="stack">
      <div className="panel">
        <div className="panel-heading">
          <div>
            <h2>Channel membership</h2>
            <p>
              Set a USD subscription price for a fixed 30-day access period.
              Included posts grant access to current and future members.
            </p>
          </div>
        </div>
        <form className="stack" onSubmit={submit}>
          <Field label="Price every 30 days (USD)">
            <input
              name="amount"
              required
              inputMode="decimal"
              defaultValue={
                offer ? (Number(offer.unit_amount) / 1_000_000).toFixed(2) : ""
              }
              placeholder="5.00"
            />
          </Field>
          {(error || save.error) && (
            <p className="form-error" role="alert">
              {error || save.error?.message}
            </p>
          )}
          {save.isSuccess && (
            <p className="notice notice-success" role="status">
              Membership offer saved.
            </p>
          )}
          <Button type="submit" busy={save.isPending}>
            Save membership offer
          </Button>
        </form>
      </div>
      <div className="panel stack">
        <h2>Delete channel</h2>
        <p className="muted">
          Deletion immediately hides the channel and retires its editorial
          permissions. Channel content is retained for 30 days before cleanup.
          Financial history remains separate.
        </p>
        <Button variant="danger" onClick={() => setDeleting(true)}>
          Delete channel
        </Button>
      </div>
      <Modal
        open={deleting}
        onOpenChange={setDeleting}
        title="Delete this channel?"
        description="The channel becomes inaccessible immediately. This does not automatically refund purchases or cancel subscriptions."
      >
        <div className="stack">
          <Field label={`Type ${channel.slug} to confirm`}>
            <input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </Field>
          {remove.error && (
            <p className="form-error" role="alert">
              {remove.error.message}
            </p>
          )}
          <div className="form-actions">
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep channel
            </Button>
            <Button
              variant="danger"
              disabled={confirmation !== channel.slug}
              busy={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Delete channel
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
export function NewChannelPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!auth.user)
    return (
      <EmptyState
        icon="users"
        title="Sign in to start a channel."
        action={<Button onClick={auth.openLogin}>Sign in</Button>}
      />
    );
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(event.currentTarget);
    try {
      const channel = await request<Channel>("/api/v1/channels", {
        method: "POST",
        body: JSON.stringify({
          slug: String(data.get("slug")).trim(),
          name: String(data.get("name")).trim(),
        }),
      });
      navigate(`/channels/${channel.id}`);
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Channel could not be created.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="page-narrow">
      <div className="page-heading">
        <h1>Become a creator</h1>
        <p>
          Your channel is your creator profile. You become its owner and can
          invite editors after it exists.
        </p>
      </div>
      <div className="panel">
        <form
          className="stack"
          onSubmit={(event) => {
            void submit(event);
          }}
        >
          <Field label="Channel name">
            <input
              name="name"
              required
              maxLength={120}
              autoFocus
              placeholder="Your creator name"
            />
          </Field>
          <Field
            label="Slug"
            hint="Lowercase letters, numbers, and dashes. AuthKit reserves and manages channel names."
          >
            <input
              name="slug"
              required
              pattern="[a-z0-9-]+"
              placeholder="my-channel"
            />
          </Field>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="form-actions">
            <Link className="button button-ghost" to="/channels">
              Cancel
            </Link>
            <Button type="submit" busy={busy}>
              Create channel
              <Icon name="arrow" size={16} />
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
