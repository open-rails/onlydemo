import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { APIError, request, postPage } from "../api";
import { NotFoundPage } from "../App";
import { useAuth } from "../session";
import type { Channel } from "../models";
import { micros, money, duration } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Add01Icon,
  ArrowLeft02Icon,
  ArrowRight02Icon,
  DollarCircleIcon,
  StarIcon,
  Tick02Icon,
  UserGroupIcon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  EmptyState,
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
import { Avatar, Cover, PostCard } from "../components/cards";
import { membershipOffer } from "../channels";
import { PostEditor } from "../components/post-editor";
import { ChannelTeam } from "../components/channel-team";
import { SlotUpload } from "../components/slot-upload";
import { useSlotVersion } from "../media";
import { MembershipDialog } from "../components/membership";

export function ChannelPage() {
  const { slug = "" } = useParams();
  const auth = useAuth();
  const client = useQueryClient();
  const [editor, setEditor] = useState(false);
  const [membership, setMembership] = useState(false);
  const [tab, setTab] = useState("posts");
  const slots = useSlotVersion();
  const channel = useQuery({
    queryKey: ["channel", slug, auth.user?.id],
    queryFn: () =>
      request<Channel>(`/api/v1/channels/${encodeURIComponent(slug)}`),
  });
  const id = channel.data?.id || "";
  const postQuery = useInfiniteQuery({
    queryKey: ["channel-posts", id, auth.user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      postPage(
        `/api/v1/posts?channel_id=${encodeURIComponent(id)}${pageParam ? "&before=" + encodeURIComponent(pageParam) : ""}`,
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !!id,
  });
  const posts = {
    ...postQuery,
    data: postQuery.data?.pages.flatMap((page) => page.data),
  };
  if (channel.isPending) return <Loading />;
  if (channel.error instanceof APIError && channel.error.status === 404)
    return <NotFoundPage />;
  if (channel.error || !channel.data)
    return (
      <ErrorState
        error={channel.error || new Error("Channel not found.")}
        retry={() => void channel.refetch()}
      />
    );
  const current = channel.data;
  const offer = membershipOffer(current);
  const target = { kind: "channel", id: current.id };
  const postCount = current.post_count ?? posts.data?.length ?? 0;
  return (
    <>
      <section className="profile">
        <Cover seed={current.id} src={slots.src(current.banner_url)} className="profile-cover">
          <Link to="/channels" className="cover-back" aria-label="Back">
            <HugeiconsIcon icon={ArrowLeft02Icon} size={20} />
          </Link>
          <div className="cover-title">
            <strong>{current.name}</strong>
            <small>
              {postCount} {postCount === 1 ? "post" : "posts"}
            </small>
          </div>
          {current.can_manage && (
            <SlotUpload target={target} slot="banner" label="Banner" onDone={slots.refresh} className="slot-upload cover-upload" />
          )}
        </Cover>
        <div className="profile-body">
          <div className="profile-top">
            <span className="avatar-slot">
              <Avatar
                name={current.name}
                seed={current.id}
                src={slots.src(current.avatar_url)}
                className="avatar-xl"
              />
              {current.can_manage && (
                <SlotUpload target={target} slot="avatar" label="Avatar" onDone={slots.refresh} />
              )}
            </span>
            <div className="inline-actions">
              {current.can_edit && (
                <Button onClick={() => setEditor(true)}>
                  <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
                  New post
                </Button>
              )}
              <Button
                variant="outline"
                size="icon-lg"
                className="rounded-full"
                aria-label="Tip creator"
              >
                <HugeiconsIcon icon={DollarCircleIcon} />
              </Button>
              <Button
                variant="outline"
                size="icon-lg"
                className="rounded-full"
                aria-label="Favorite creator"
              >
                <HugeiconsIcon icon={StarIcon} />
              </Button>
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
                <Button
                  variant="secondary"
                  size="lg"
                  className="w-full rounded-full text-success"
                  disabled
                >
                  <HugeiconsIcon icon={Tick02Icon} data-icon="inline-start" />
                  Subscribed
                </Button>
              ) : offer ? (
                <Button
                  size="lg"
                  className="h-12 w-full justify-between rounded-full px-6 uppercase"
                  onClick={() => setMembership(true)}
                >
                  <span>Subscribe</span>
                  <span>
                    {money(offer.unit_amount, offer.currency)}{" "}
                    {duration(offer.access_duration_hours).replace("every ", "/ ")}
                  </span>
                </Button>
              ) : (
                <p className="muted">This creator has no subscription yet.</p>
              )}
            </div>
          )}
        </div>
      </section>
      <Tabs value={tab} onValueChange={(value) => setTab(String(value))} className="my-4">
        <TabsList variant="line" aria-label="Channel sections" className="w-full">
          <TabsTrigger value="posts">
            {postCount} {postCount === 1 ? "Post" : "Posts"}
          </TabsTrigger>
          <TabsTrigger value="about">About</TabsTrigger>
          {current.can_edit && <TabsTrigger value="team">Team</TabsTrigger>}
          {current.can_manage && (
            <TabsTrigger value="settings">Settings</TabsTrigger>
          )}
        </TabsList>
      </Tabs>
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
            variant="outline"
            disabled={posts.isFetchingNextPage}
            onClick={() => void posts.fetchNextPage()}
          >
            {posts.isFetchingNextPage && <Spinner data-icon="inline-start" />}
            Older posts
          </Button>
        </div>
      )}
      {tab === "about" && (
        <Card>
          <CardHeader>
            <CardTitle>About {current.name}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
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
          </CardContent>
        </Card>
      )}
      {tab === "team" && current.can_edit && <ChannelTeam channel={current} />}
      {tab === "settings" && current.can_manage && (
        <ChannelSettings
          channel={current}
          onUpdated={() =>
            void client.invalidateQueries({ queryKey: ["channel", slug] })
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
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Channel membership</CardTitle>
          <CardDescription>
            Set a USD subscription price for a fixed 30-day access period.
            Included posts grant access to current and future members.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="membership-amount">
                  Price every 30 days (USD)
                </FieldLabel>
                <Input
                  id="membership-amount"
                  name="amount"
                  required
                  inputMode="decimal"
                  defaultValue={
                    offer
                      ? (Number(offer.unit_amount) / 1_000_000).toFixed(2)
                      : ""
                  }
                  placeholder="5.00"
                />
              </Field>
              <FormError>{error || save.error?.message}</FormError>
              {save.isSuccess && (
                <Alert role="status">
                  <HugeiconsIcon icon={Tick02Icon} />
                  <AlertDescription>Membership offer saved.</AlertDescription>
                </Alert>
              )}
              <Button type="submit" className="self-start" disabled={save.isPending}>
                {save.isPending && <Spinner data-icon="inline-start" />}
                Save membership offer
              </Button>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Delete channel</CardTitle>
          <CardDescription>
            Deletion immediately hides the channel and retires its editorial
            permissions. Channel content is retained for 30 days before
            cleanup. Financial history remains separate.
          </CardDescription>
        </CardHeader>
        <CardFooter>
          <Button variant="destructive" onClick={() => setDeleting(true)}>
            Delete channel
          </Button>
        </CardFooter>
      </Card>
      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this channel?</DialogTitle>
            <DialogDescription>
              The channel becomes inaccessible immediately. This does not
              automatically refund purchases or cancel subscriptions.
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="delete-channel-confirm">
              Type {channel.slug} to confirm
            </FieldLabel>
            <Input
              id="delete-channel-confirm"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </Field>
          <FormError>{remove.error?.message}</FormError>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep channel
            </Button>
            <Button
              variant="destructive"
              disabled={confirmation !== channel.slug || remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending && <Spinner data-icon="inline-start" />}
              Delete channel
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
        icon={UserGroupIcon}
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
      navigate(`/channels/${channel.slug}`);
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
      <Card>
        <CardContent>
          <form
            onSubmit={(event) => {
              void submit(event);
            }}
          >
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="channel-name">Channel name</FieldLabel>
                <Input
                  id="channel-name"
                  name="name"
                  required
                  maxLength={120}
                  autoFocus
                  placeholder="Your creator name"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="channel-slug">Slug</FieldLabel>
                <Input
                  id="channel-slug"
                  name="slug"
                  required
                  pattern="[a-z0-9\-]+"
                  placeholder="my-channel"
                />
                <FieldDescription>
                  Lowercase letters, numbers, and dashes. Channel names are
                  unique, and some are reserved.
                </FieldDescription>
              </Field>
              <FormError>{error}</FormError>
              <div className="form-actions">
                <Button
                  variant="ghost"
                  nativeButton={false}
                  render={<Link to="/channels" />}
                >
                  Cancel
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner data-icon="inline-start" />}
                  Create channel
                  <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
                </Button>
              </div>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
