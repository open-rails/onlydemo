import { useEffect, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ContentKitError, type EntityDetail } from "@openrails/contentkit-ui/client";
import { ContentGrid, EntityBanner, EntityImageEditor } from "@openrails/contentkit-ui";
import { useCanonicalContent, useEntity } from "@openrails/contentkit-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { Add01Icon, ArrowRight02Icon, UserGroupIcon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
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
import { request } from "../api";
import type { Product } from "@openrails/billing-ui/client";
import { accessChanged } from "../purchases";
import { ProductSettings } from "../components/product-settings";
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
  const read = useEntity(id);
  const roles = useChannelRoles();
  const group = read.entity;
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
      <EntityBanner entity={group} onCheckout={checkout}>
        {role && (
          <Button nativeButton={false} render={<Link to={newPostPath(group.slug)} />}>
            <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
            New post
          </Button>
        )}
        {group.access?.member && <LeaveFreeMembership id={group.id} />}
      </EntityBanner>
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

function LeaveFreeMembership({id}: {id: string}) {
  const query = useQueryClient();
  const product = useQuery({queryKey: ["product", `onlydemo:group:${id}`], queryFn: () => request<Product | null>(`/api/v1/products?key=onlydemo:group:${id}`)});
  const [busy,setBusy] = useState(false);
  const [error,setError] = useState("");
  if (product.data?.description !== "OnlyDemo free membership") return null;
  return <div><Button variant="outline" disabled={busy} onClick={() => {setBusy(true);void request(`/api/v1/memberships/${id}`,{method:"DELETE"}).then(() => {accessChanged();void query.invalidateQueries();}).catch((cause: unknown) => setError(cause instanceof Error ? cause.message : "Could not leave membership.")).finally(() => setBusy(false));}}>Leave free membership</Button><FormError>{error}</FormError></div>;
}

function ChannelSettings({ group, onUpdated }: { group: EntityDetail; onUpdated: () => void }) {
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
      await request(`/api/v1/products/entities/${group.id}`, {method: "DELETE"});
      await contentkit.entities.remove(group.id);
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
          <EntityImageEditor entity={group} slot="cover" />
          <EntityImageEditor entity={group} slot="avatar" />
        </CardContent>
      </Card>
      <ProductSettings kind="entities" id={group.id} onUpdated={onUpdated} />
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
      const [channel] = await contentkit.entities.create([
        {
          kind: "channel",
          slug: String(data.get("slug")).trim().toLowerCase(),
          names: [{ language: "en", kind: "name", name: String(data.get("name")).trim() }],
        },
      ]);
      await client.invalidateQueries();
      const target = thenPost ? newPostPath(channel.slug) : channelPath(await contentkit.entities.get(channel.id));
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
