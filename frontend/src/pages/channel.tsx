import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useEffect, useState, type FormEvent } from "react";
import { cn } from "cn";
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
  Tick02Icon,
  UserGroupIcon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button, buttonVariants } from "@/components/ui/button";
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
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  EmptyState,
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
import { Avatar, Cover } from "../components/cards";
import { PostView } from "../components/post-view";
import { membershipOffer, myChannelsKey, rememberPostChannel } from "../channels";
import { ChannelTeam } from "../components/channel-team";
import { SlotEditError, SlotEditMenu, SlotEditor } from "@openrails/contentkit-upload/ui";
import { channelRef, useSlot, useSlotSaved } from "../media";
import { subscribeLabel, usePay } from "../components/pay";
import { channelsPath, channelPath, newPostPath } from "../paths";

export function ChannelPage() {
  const { channel: slug = "" } = useParams();
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const pay = usePay();
  const [tab, setTab] = useState("posts");
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
  // Managers read the full slot manifests (edit, source size) to re-crop.
  const manage = !!channel.data?.can_manage;
  const avatar = useSlot(channelRef(id), "avatar", channel.data?.avatar, manage);
  const cover = useSlot(channelRef(id), "cover", channel.data?.cover, manage);
  const saved = useSlotSaved();
  const canonical = channel.data?.slug;
  const editable = channel.data?.can_edit ? channel.data.id : "";
  const userID = auth.user?.id;
  useEffect(() => {
    if (userID && editable) rememberPostChannel(userID, editable);
  }, [userID, editable]);
  useEffect(() => {
    // A former slug still resolves; show the current URL.
    if (canonical && canonical !== slug)
      navigate(channelPath(canonical), { replace: true });
  }, [canonical, slug, navigate]);
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
  const postCount = current.post_count ?? posts.data?.length ?? 0;
  return (
    <>
      <section className="profile">
        <Cover seed={current.id} image={cover} sizes="(min-width: 768px) 720px, 100vw" className="profile-cover">
          <Link to={channelsPath} className="cover-back" aria-label="Back">
            <HugeiconsIcon icon={ArrowLeft02Icon} size={20} />
          </Link>
          <div className="cover-title">
            <strong>{current.name}</strong>
            <small>
              {postCount} {postCount === 1 ? "post" : "posts"}
            </small>
          </div>
          {current.can_manage && (
            <SlotEditor
              item={channelRef(current.id)}
              slot="cover"
              manifest={cover}
              aspect={3}
              targetWidth={3000}
              onChange={(m) => saved(channelRef(current.id), "cover", m)}
            >
              <SlotEditMenu
                label="Edit cover"
                render={
                  <button
                    type="button"
                    className={cn(
                      buttonVariants({ variant: "secondary", size: "sm" }),
                      "cover-edit rounded-full border-white/20 bg-black/55 text-white backdrop-blur-md hover:bg-black/70",
                    )}
                  />
                }
              />
              <SlotEditError className="slot-error" />
            </SlotEditor>
          )}
        </Cover>
        <div className="profile-body">
          <div className="profile-top">
            <span className="avatar-slot">
              <Avatar
                name={current.name}
                seed={current.id}
                image={avatar}
                sizes="112px"
                className="avatar-xl"
              />
              {current.can_manage && (
                <SlotEditor
                  item={channelRef(current.id)}
                  slot="avatar"
                  manifest={avatar}
                  aspect={1}
                  targetWidth={512}
                  onChange={(m) => saved(channelRef(current.id), "avatar", m)}
                >
                  <SlotEditMenu
                    label="Change avatar"
                    iconOnly
                    render={
                      <button
                        type="button"
                        className={cn(
                          buttonVariants({ variant: "secondary", size: "icon-sm" }),
                          "avatar-edit rounded-full border-2 border-card shadow-sm",
                        )}
                      />
                    }
                  />
                  <SlotEditError className="slot-error" />
                </SlotEditor>
              )}
            </span>
            <div className="inline-actions">
              {current.can_edit && (
                <Button
                  nativeButton={false}
                  render={<Link to={newPostPath(current.slug)} />}
                >
                  <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
                  New post
                </Button>
              )}
            </div>
          </div>
          <h1 className="profile-name">
            {current.name}
          </h1>
          <p className="handle">
            @{current.slug}
          </p>
          {current.description && (
            <p className="profile-bio">{current.description}</p>
          )}
          {!current.can_edit && current.membership.status !== "none" && (
            <MembershipBox
              channel={current}
              onSubscribe={() => {
                const offer = membershipOffer(current);
                if (offer) pay({ kind: "membership", id: current.id, merchant: current.name, offer });
              }}
              onChanged={() => void client.invalidateQueries()}
            />
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
              <PostView
                post={{ ...post, channel_name: current.name }}
                handle={current.slug}
                membership={current.membership}
                key={post.id}
              />
            ))}
          </div>
        ) : (
          <EmptyState
            title="No posts yet."
            action={
              current.can_edit ? (
                <Button
                  nativeButton={false}
                  render={<Link to={newPostPath(current.slug)} />}
                >
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
            Members read posts marked “Included with membership”. Some posts
            are sold separately. Every purchased post keeps permanent access,
            even after membership ends.
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
    </>
  );
}
function MembershipBox({
  channel,
  onSubscribe,
  onChanged,
}: {
  channel: Channel;
  onSubscribe: () => void;
  onChanged: () => void;
}) {
  const auth = useAuth();
  const membership = channel.membership;
  const offer = membershipOffer(channel);
  const change = useMutation({
    mutationFn: (action: "join" | "leave") =>
      request(`/api/v1/channels/${channel.id}/${action}`, { method: "POST" }),
    onSuccess: onChanged,
  });
  const pill = "h-12 w-full rounded-full px-6";
  return (
    <div className="subscribe-box">
      <span className="subscribe-label">Membership</span>
      {membership.member ? (
        <>
          <Button variant="secondary" size="lg" className={cn(pill, "text-success")} disabled>
            <HugeiconsIcon icon={Tick02Icon} data-icon="inline-start" />
            You're a member
          </Button>
          {membership.free_member && (
            <Button
              variant="link"
              size="sm"
              disabled={change.isPending}
              onClick={() => change.mutate("leave")}
            >
              Leave membership
            </Button>
          )}
        </>
      ) : membership.status === "closed" ? (
        <Button variant="secondary" size="lg" className={pill} disabled>
          Membership closed
        </Button>
      ) : membership.free ? (
        <Button
          size="lg"
          className={cn(pill, "uppercase")}
          disabled={change.isPending}
          onClick={() => (auth.user ? change.mutate("join") : auth.openLogin())}
        >
          {change.isPending && <Spinner data-icon="inline-start" />}
          Join free
        </Button>
      ) : offer ? (
        <Button
          size="lg"
          className={pill}
          onClick={onSubscribe}
        >
          {subscribeLabel(offer)}
        </Button>
      ) : (
        <Button variant="secondary" size="lg" className={pill} disabled>
          Price pending
        </Button>
      )}
      <FormError>{change.error?.message}</FormError>
    </div>
  );
}
function MembershipSettings({
  channel,
  onUpdated,
}: {
  channel: Channel;
  onUpdated: () => void;
}) {
  const membership = channel.membership;
  const offer = membershipOffer(channel);
  const [enabled, setEnabled] = useState(membership.status === "open");
  const [free, setFree] = useState(
    membership.status === "none" ? false : membership.free,
  );
  const [amount, setAmount] = useState(
    offer ? (Number(offer.unit_amount) / 1_000_000).toFixed(2) : "",
  );
  const [error, setError] = useState("");
  const save = useMutation({
    mutationFn: (body: object) =>
      request(`/api/v1/channels/${channel.id}/membership`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: onUpdated,
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    if (!enabled) return save.mutate({ enabled: false, price: null });
    if (free) return save.mutate({ enabled: true, price: null });
    try {
      const unit_amount = micros(amount.trim());
      if (BigInt(unit_amount) < 1_000_000n)
        throw new Error("A paid membership costs at least $1.00.");
      save.mutate({ enabled: true, price: { unit_amount, currency: "USD" } });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Invalid price.");
    }
  };
  const status =
    membership.status === "none"
      ? "This channel has no membership. Posts can be free or sold individually."
      : membership.status === "closed"
        ? "Closed to new members. Existing members keep access and renew at the price they accepted."
        : membership.free
          ? "Open: anyone can join free."
          : offer
            ? `Open: ${money(offer.unit_amount, offer.currency)} ${duration(offer.access_duration_hours)}.`
            : "Open: price pending.";
  const paidToFree =
    enabled && free && membership.status !== "none" && !membership.free;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Channel membership</CardTitle>
        <CardDescription>{status}</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit}>
          <FieldGroup>
            <Field orientation="horizontal">
              <Switch
                id="membership-enabled"
                checked={enabled}
                onCheckedChange={setEnabled}
              />
              <FieldContent>
                <FieldLabel htmlFor="membership-enabled">
                  Accept new members
                </FieldLabel>
                <FieldDescription>
                  Turning this off stops new joins. Existing members keep
                  access and see new membership posts.
                </FieldDescription>
              </FieldContent>
            </Field>
            {enabled && (
              <FieldSet>
                <FieldLegend variant="label">Price</FieldLegend>
                <RadioGroup
                  className="grid gap-3 sm:grid-cols-2"
                  value={free ? "free" : "paid"}
                  onValueChange={(value) => setFree(value === "free")}
                >
                  <FieldLabel htmlFor="membership-free">
                    <Field orientation="horizontal">
                      <FieldContent>
                        <FieldTitle>Free</FieldTitle>
                        <FieldDescription>Join without a card.</FieldDescription>
                      </FieldContent>
                      <RadioGroupItem value="free" id="membership-free" />
                    </Field>
                  </FieldLabel>
                  <FieldLabel htmlFor="membership-paid">
                    <Field orientation="horizontal">
                      <FieldContent>
                        <FieldTitle>Paid</FieldTitle>
                        <FieldDescription>Renews automatically.</FieldDescription>
                      </FieldContent>
                      <RadioGroupItem value="paid" id="membership-paid" />
                    </Field>
                  </FieldLabel>
                </RadioGroup>
              </FieldSet>
            )}
            {enabled && !free && (
              <Field>
                <FieldLabel htmlFor="membership-amount">
                  Price {duration(offer?.access_duration_hours ?? 720)} (USD)
                </FieldLabel>
                <Input
                  id="membership-amount"
                  required
                  inputMode="decimal"
                  pattern="[0-9]+(\.[0-9]{1,2})?"
                  value={amount}
                  onChange={(event) => setAmount(event.target.value)}
                  placeholder="5.00"
                />
                <FieldDescription>
                  Minimum $1.00. New members pay the new price; existing
                  members keep the price they accepted.
                </FieldDescription>
              </Field>
            )}
            {paidToFree && (
              <Alert>
                <AlertDescription>
                  Paid members keep access for free; their subscriptions stop
                  renewing at the end of the current paid period.
                </AlertDescription>
              </Alert>
            )}
            <FormError>{error || save.error?.message}</FormError>
            {save.isSuccess && (
              <Alert role="status">
                <HugeiconsIcon icon={Tick02Icon} />
                <AlertDescription>Membership saved.</AlertDescription>
              </Alert>
            )}
            <Button
              type="submit"
              className="self-start"
              disabled={
                save.isPending || (!enabled && membership.status !== "open")
              }
            >
              {save.isPending && <Spinner data-icon="inline-start" />}
              {membership.status === "none" ? "Create membership" : "Save membership"}
            </Button>
          </FieldGroup>
        </form>
      </CardContent>
    </Card>
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
  const [deleting, setDeleting] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const remove = useMutation({
    mutationFn: () =>
      request(`/api/v1/channels/${channel.id}`, { method: "DELETE" }),
    onSuccess: () => navigate("/me?tab=channels"),
  });
  return (
    <div className="flex flex-col gap-4">
      <MembershipSettings channel={channel} onUpdated={onUpdated} />
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
  const client = useQueryClient();
  // Arrived from Post without a channel: continue to the composer after.
  const thenPost = useSearchParams()[0].get("then") === "post";
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
      await client.invalidateQueries({ queryKey: myChannelsKey(auth.user?.id) });
      navigate(thenPost ? newPostPath(channel.slug) : channelPath(channel.slug), {
        replace: thenPost,
      });
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
        <h1>{thenPost ? "Create a channel to post" : "Become a creator"}</h1>
        <p>
          {thenPost
            ? "Posts are published to a channel, your creator profile. You need one before you can post; your new post opens right after."
            : "Your channel is your creator profile. You become its owner and can invite editors after it exists."}
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
                  render={<Link to={channelsPath} />}
                >
                  Cancel
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner data-icon="inline-start" />}
                  {thenPost ? "Create channel and write post" : "Create channel"}
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
