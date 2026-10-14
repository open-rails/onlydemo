import { useEffect, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ContentKitError, type GroupDetail } from "@openrails/contentkit-ui/client";
import { ContentGrid, GroupBanner, GroupImageEditor } from "@openrails/contentkit-ui";
import { useCanonicalContent, useGroup } from "@openrails/contentkit-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { Add01Icon, ArrowRight02Icon, UserGroupIcon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet, FieldTitle } from "@/components/ui/field";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { NotFoundPage } from "../App";
import { EmptyState, ErrorState, FormError, Loading } from "../components/states";
import { ChannelTeam } from "../components/channel-team";
import { useCheckout } from "../components/pay";
import { Post } from "./explore";
import { contentkit } from "../media";
import { useAuth } from "../session";
import { rememberPostChannel, useChannelRoles } from "../roles";
import { amount as price, money, duration } from "../format";
import { channelPath, channelsPath, contentURLs, newPostPath, parseCode } from "../paths";

// A channel's page: ContentKit's banner and membership box, its posts
// (owner_group), and for its team the AuthKit roster and its settings.
export function ChannelPage() {
  const { code: rawCode = "", language } = useParams();
  const code = parseCode(rawCode);
  const supported = !language || contentURLs.languages.includes(language);
  const found = useQuery({
    queryKey: ["channel-code", code, language],
    queryFn: () => contentkit.codes.resolve(code!, { lang: language }),
    enabled: !!code && supported,
  });
  const auth = useAuth();
  const [tab, setTab] = useState("posts");
  const id = found.data?.content_kind === "channel" ? found.data.content_id : null;
  const read = useGroup(id);
  const roles = useChannelRoles();
  const group = read.group;
  const role = id ? roles.data?.get(id) : undefined;
  const checkout = useCheckout(group?.name ?? "Creator");
  const userID = auth.user?.id;
  const location = useLocation();
  useCanonicalContent(id ? found.data : undefined, {
    title: group?.name,
    location: location.pathname + location.search + location.hash,
  });
  useEffect(() => {
    if (userID && id && role) rememberPostChannel(userID, id);
  }, [userID, id, role]);
  // Lists reload on every change; show Loading only before the first read.
  if (!code || !supported) return <NotFoundPage />;
  if ((found.isPending && !id) || (id && !group && !read.error)) return <Loading />;
  if (!id) return found.error && !(found.error instanceof ContentKitError && found.error.status === 404) ? <ErrorState error={found.error} retry={() => void found.refetch()} /> : <NotFoundPage />;
  if (read.error || !group) return <ErrorState error={read.error ?? new Error("Channel not found.")} retry={read.reload} />;
  const manage = role === "owner";
  return (
    <>
      <GroupBanner group={group} onCheckout={checkout}>
        {role && (
          <Button nativeButton={false} render={<Link to={newPostPath(group.slug)} />}>
            <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
            New post
          </Button>
        )}
      </GroupBanner>
      <Tabs value={tab} onValueChange={(value) => setTab(String(value))} className="my-4">
        <TabsList variant="line" aria-label="Channel sections" className="w-full">
          <TabsTrigger value="posts">Posts</TabsTrigger>
          {role && <TabsTrigger value="team">Team</TabsTrigger>}
          {manage && <TabsTrigger value="settings">Settings</TabsTrigger>}
        </TabsList>
      </Tabs>
      {tab === "posts" && (
        <ContentGrid
          className="feed"
          surface="channel"
          query={{ kind: "post", owner_group: group.id, sort: "newest", mode: "lock" }}
          renderCard={(card) => <Post card={card} owner={false} />}
          empty={
            <EmptyState
              title="No posts yet."
              action={
                role ? (
                  <Button nativeButton={false} render={<Link to={newPostPath(group.slug)} />}>
                    Write the first post
                  </Button>
                ) : undefined
              }
            >
              Check back soon for the first post from this creator.
            </EmptyState>
          }
        />
      )}
      {tab === "team" && role && <ChannelTeam channel={{ id: group.id, can_manage: manage }} />}
      {tab === "settings" && manage && <ChannelSettings group={group} onUpdated={read.reload} />}
    </>
  );
}

// The membership a channel sells: ContentKit's group offer (free or renewing
// monthly), or none. Its owner reads it as GroupDetail.offer; keyed by that
// offer, the form starts over from each saved one.
function MembershipSettings({ group, onUpdated }: { group: GroupDetail; onUpdated: () => void }) {
  const terms = group.offer?.state === "active" ? group.offer.terms : undefined;
  const [enabled, setEnabled] = useState(!!terms);
  const free0 = !!terms && Number(terms.amount) === 0;
  const [free, setFree] = useState(free0);
  const [amount, setAmount] = useState(terms && !free0 ? terms.amount : "");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setSaving(true);
    try {
      if (!enabled) await contentkit.offers.withdrawGroup(group.id);
      else await contentkit.offers.sellGroup(group.id, free ? { amount: "0", currency: "USD" } : { amount: price(amount.trim(), 1), currency: "USD", renew_hours: 720 });
      toast.success("Membership saved.");
      onUpdated();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the membership.");
    } finally {
      setSaving(false);
    }
  };
  const status = !terms
    ? "This channel sells no membership. Posts can be free or sold individually; existing members keep access."
    : free0
      ? "Open: anyone can join free."
      : `Open: ${money(String(Number(terms.amount) * 1_000_000), terms.currency)} ${duration(terms.renew_hours)}.`;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Channel membership</CardTitle>
        <CardDescription>{status}</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={(event) => void save(event)}>
          <FieldGroup>
            <Field orientation="horizontal">
              <Switch id="membership-enabled" checked={enabled} onCheckedChange={setEnabled} />
              <FieldContent>
                <FieldLabel htmlFor="membership-enabled">Accept new members</FieldLabel>
                <FieldDescription>Turning this off stops new joins. Existing members keep access and see new membership posts.</FieldDescription>
              </FieldContent>
            </Field>
            {enabled && (
              <FieldSet>
                <FieldLegend variant="label">Price</FieldLegend>
                <RadioGroup className="grid gap-3 sm:grid-cols-2" value={free ? "free" : "paid"} onValueChange={(value) => setFree(value === "free")}>
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
                        <FieldDescription>Renews every 30 days.</FieldDescription>
                      </FieldContent>
                      <RadioGroupItem value="paid" id="membership-paid" />
                    </Field>
                  </FieldLabel>
                </RadioGroup>
              </FieldSet>
            )}
            {enabled && !free && (
              <Field>
                <FieldLabel htmlFor="membership-amount">Price every 30 days (USD)</FieldLabel>
                <Input
                  id="membership-amount"
                  required
                  inputMode="decimal"
                  pattern="[0-9]+(\.[0-9]{1,2})?"
                  value={amount}
                  onChange={(event) => setAmount(event.target.value)}
                  placeholder="5.00"
                />
                <FieldDescription>Minimum $1.00. New members pay the new price; existing members keep the price they accepted.</FieldDescription>
              </Field>
            )}
            <FormError>{error}</FormError>
            <Button type="submit" className="self-start" disabled={saving || (!enabled && !terms)}>
              {saving && <Spinner data-icon="inline-start" />}
              {terms ? "Save membership" : "Create membership"}
            </Button>
          </FieldGroup>
        </form>
      </CardContent>
    </Card>
  );
}

function ChannelSettings({ group, onUpdated }: { group: GroupDetail; onUpdated: () => void }) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [removing, setRemoving] = useState(false);
  const [error, setError] = useState("");
  const remove = async () => {
    setRemoving(true);
    setError("");
    try {
      await contentkit.groups.remove(group.id);
      await client.invalidateQueries();
      navigate("/me?tab=channels");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not delete the channel.");
    } finally {
      setRemoving(false);
    }
  };
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Images</CardTitle>
          <CardDescription>The cover across the channel's page and its avatar.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <GroupImageEditor group={group} slot="cover" />
          <GroupImageEditor group={group} slot="avatar" />
        </CardContent>
      </Card>
      <MembershipSettings key={group.offer ? `${group.offer.id}:${group.offer.revision}` : "none"} group={group} onUpdated={onUpdated} />
      <Card>
        <CardHeader>
          <CardTitle>Delete channel</CardTitle>
          <CardDescription>
            Deletion immediately hides the channel, stops selling its membership and retires its editorial permissions. Financial history remains separate.
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
            <DialogDescription>The channel becomes inaccessible immediately. This does not refund purchases or cancel subscriptions.</DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="delete-channel-confirm">Type {group.slug} to confirm</FieldLabel>
            <Input id="delete-channel-confirm" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} />
          </Field>
          <FormError>{error}</FormError>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep channel
            </Button>
            <Button variant="destructive" disabled={confirmation !== group.slug || removing} onClick={() => void remove()}>
              {removing && <Spinner data-icon="inline-start" />}
              Delete channel
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// A creator's new channel: ContentKit makes it and its AuthKit group, the
// creator its owner.
export function NewChannelPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const thenPost = useSearchParams()[0].get("then") === "post";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!auth.user)
    return <EmptyState icon={UserGroupIcon} title="Sign in to start a channel." action={<Button onClick={auth.openLogin}>Sign in</Button>} />;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(event.currentTarget);
    try {
      const [channel] = await contentkit.groups.create([
        {
          kind: "channel",
          slug: String(data.get("slug")).trim().toLowerCase(),
          names: [{ language: "en", kind: "name", name: String(data.get("name")).trim() }],
        },
      ]);
      await client.invalidateQueries();
      const target = thenPost ? newPostPath(channel.slug) : channelPath(await contentkit.groups.get(channel.id));
      navigate(target, { replace: thenPost });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Channel could not be created.");
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
          <form onSubmit={(event) => void submit(event)}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="channel-name">Channel name</FieldLabel>
                <Input id="channel-name" name="name" required maxLength={120} autoFocus placeholder="Your creator name" />
              </Field>
              <Field>
                <FieldLabel htmlFor="channel-slug">Handle</FieldLabel>
                <Input id="channel-slug" name="slug" required pattern="[a-z0-9\-]+" placeholder="my-channel" />
                <FieldDescription>Lowercase letters, numbers, and dashes. Handles are unique.</FieldDescription>
              </Field>
              <FormError>{error}</FormError>
              <div className="form-actions">
                <Button variant="ghost" nativeButton={false} render={<Link to={channelsPath} />}>
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
