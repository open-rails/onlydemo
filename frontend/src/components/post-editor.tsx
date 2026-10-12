import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { loadPost, posts } from "../content";
import type { AccessPolicy, Channel, Post } from "../models";
import { amount } from "../format";
import { HugeiconsIcon } from "@hugeicons/react";
import { ArrowRight02Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
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
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { FormError } from "./states";
import { MemberStar } from "./policy-badge";
import { MediaFolderEditor } from "@openrails/contentkit-ui";
import { useMediaFolder, useMediaRead } from "@openrails/contentkit-ui/react";
import { postPaths, postRef } from "../media";

const policies: Array<{ value: AccessPolicy; title: string; detail: string }> =
  [
    {
      value: "public",
      title: "Free to read",
      detail: "Anyone can read, without an account.",
    },
    {
      value: "membership",
      title: "Included with membership",
      detail: "Current and future members get access while they are members.",
    },
    {
      value: "members_ppv",
      title: "Purchase for members",
      detail: "Membership required to buy. Purchased access is permanent.",
    },
    {
      value: "ppv",
      title: "Purchase for anyone",
      detail: "A one-time purchase grants permanent access.",
    },
  ];
const membershipPolicies: AccessPolicy[] = ["membership", "members_ppv"];
// Editing an existing post; new posts are written on the /post/new page.
export function PostEditor({
  post,
  hasMembership,
  open,
  onClose,
  onSaved,
}: {
  post: Post;
  hasMembership: boolean;
  open: boolean;
  onClose: () => void;
  onSaved: (post: Post) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(value) => !value && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Edit post</DialogTitle>
          <DialogDescription>
            Changes publish immediately to your channel.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <PostForm
            key={post.id}
            channelID={post.channel_id}
            hasMembership={hasMembership}
            post={post}
            onSaved={(saved) => {
              onSaved(saved);
              onClose();
            }}
            onCancel={onClose}
            Actions={DialogFooter}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
export function PostForm({
  channelID,
  hasMembership,
  post,
  channels,
  onChannel,
  onSaved,
  onCancel,
  onDraft,
  confirming = false,
  setConfirming,
  onDiscarded,
  Actions = FormActions,
}: {
  channelID: string;
  hasMembership: boolean;
  post?: Post;
  // Several postable channels show a picker, locked once a draft holds media.
  channels?: Channel[];
  onChannel?: (channel: Channel) => void;
  onSaved: (post: Post) => void;
  onCancel: () => void;
  onDraft?: (has: boolean) => void;
  confirming?: boolean;
  setConfirming?: (v: boolean) => void;
  onDiscarded?: () => void;
  Actions?: (props: { children: ReactNode }) => ReactNode;
}) {
  const client = useQueryClient();
  // New posts upload into a draft created with the first file (files wait
  // for it in the folder); Publish turns it into the post, Cancel deletes it
  // with its media.
  const [draft, setDraft] = useState<{ id: string; version: string }>();
  const folder = useMediaFolder(draft ? postRef(draft.id, draft.version) : null, { paths: postPaths, commit: "auto" });
  const createDraft = useMutation({ mutationFn: () => posts.draft(channelID, "public") });
  const { mutate: create, isPending: creating } = createDraft;
  const waiting = folder.waiting.length;
  // A failed creation is tried again when more files are added.
  const failedAt = useRef(-1);
  useEffect(() => {
    if (post || draft || waiting === 0 || creating || failedAt.current === waiting) return;
    create(undefined, {
      onSuccess: (created) => {
        setDraft(created);
        onDraft?.(true);
      },
      onError: () => (failedAt.current = waiting),
    });
  }, [post, draft, waiting, creating, create, onDraft]);
  const discard = useMutation({
    mutationFn: async () => {
      folder.discard();
      if (draft) await posts.remove(draft.id);
    },
    onSuccess: () => onDiscarded?.(),
  });

  const [chosen, setPolicy] = useState<AccessPolicy>(
    post?.access_policy || "public",
  );
  // A new post moved to a channel without membership falls back to public.
  const policy =
    !post && !hasMembership && membershipPolicies.includes(chosen)
      ? "public"
      : chosen;
  const [validation, setValidation] = useState("");
  // Text is optional once the post has an image or video.
  const [hasText, setHasText] = useState(!!(post?.title.trim() || post?.body?.trim()));
  // A saved post's uploads; a new post has its draft's.
  const saved = useMediaRead(postRef(post?.id ?? "", post?.version ?? ""), { editor: true, read: post ? undefined : null });
  const hasMedia = folder.fileCount > 0 || !!saved.read?.files.some((f) => f.upload && !f.unattached);
  const mediaBusy = folder.busy || creating;
  const needsText = !hasMedia && !hasText;
  const offer = post?.offers?.find((value) => value.kind !== "recurring");
  const save = useMutation({
    mutationFn: async (input: { title: string; body: string; policy: AccessPolicy; price?: string }) => {
      const id = post?.id ?? draft?.id ?? (await posts.draft(channelID, input.policy)).id;
      await posts.save(id, post?.channel_id ?? channelID, input, !post);
      return (await loadPost(id, true)).post;
    },
    onSuccess: async (saved) => {
      // Follow a new title's slug (or open the new post) before refetching.
      onSaved(saved);
      await client.invalidateQueries();
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setValidation("");
    if (needsText) {
      setValidation(textRequired);
      return;
    }
    const data = new FormData(event.currentTarget);
    try {
      save.mutate({
        title: String(data.get("title")).trim(),
        body: String(data.get("body")),
        policy,
        ...(["ppv", "members_ppv"].includes(policy) ? { price: amount(String(data.get("amount")), 0.5) } : {}),
      });
    } catch (error) {
      setValidation(
        error instanceof Error ? error.message : "Please check your fields.",
      );
    }
  };
  return (
    <form
      onSubmit={submit}
      onInput={(e) => {
        const f = e.currentTarget.elements as unknown as Record<string, HTMLInputElement | undefined>;
        setHasText(!!(f.title?.value.trim() || f.body?.value.trim()));
      }}
    >
      <FieldGroup>
        {channels && channels.length > 1 && (
          <Field>
            <FieldLabel htmlFor="post-channel">Channel</FieldLabel>
            <NativeSelect
              id="post-channel"
              className="w-full"
              value={channelID}
              disabled={!!draft || creating || folder.fileCount > 0}
              onChange={(event) => {
                const next = channels.find((c) => c.id === event.target.value);
                if (next) onChannel?.(next);
              }}
            >
              {channels.map((channel) => (
                <NativeSelectOption key={channel.id} value={channel.id}>
                  {channel.name} (@{channel.slug})
                </NativeSelectOption>
              ))}
            </NativeSelect>
            {draft && (
              <FieldDescription>
                Media is uploading to this channel. Discard the post to publish
                somewhere else.
              </FieldDescription>
            )}
          </Field>
        )}
        <Field>
          <FieldLabel htmlFor="post-title">Title</FieldLabel>
          <Input
            id="post-title"
            name="title"
            maxLength={200}
            autoFocus
            defaultValue={post?.title}
          />
          <FieldDescription>The post's address follows its title; old links keep working.</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="post-body">Story</FieldLabel>
          <Textarea
            id="post-body"
            name="body"
            className="min-h-48"
            defaultValue={post?.body}
            placeholder={hasMedia ? "Optional" : "Start writing…"}
          />
          {needsText && <FieldDescription data-testid="text-required">{textRequired}</FieldDescription>}
        </Field>
        {!post && (
          <>
            <MediaFolderEditor folder={folder} label="Images and videos" className="media-editor in-composer" />
            <FormError>{createDraft.error?.message}</FormError>
          </>
        )}
        <FieldSet>
          <FieldLegend variant="label">Who can read this?</FieldLegend>
          <RadioGroup
            className="grid gap-3 sm:grid-cols-2"
            value={policy}
            onValueChange={(value) => setPolicy(value as AccessPolicy)}
          >
            {policies
              .filter(
                (choice) =>
                  hasMembership ||
                  choice.value === policy ||
                  !membershipPolicies.includes(choice.value),
              )
              .map((choice) => (
              <FieldLabel htmlFor={`policy-${choice.value}`} key={choice.value}>
                <Field orientation="horizontal">
                  <FieldContent>
                    <FieldTitle>
                      {membershipPolicies.includes(choice.value) && (
                        <MemberStar className="size-3.5 text-amber-500 dark:text-amber-300" />
                      )}
                      {choice.title}
                    </FieldTitle>
                    <FieldDescription>{choice.detail}</FieldDescription>
                  </FieldContent>
                  <RadioGroupItem
                    value={choice.value}
                    id={`policy-${choice.value}`}
                  />
                </Field>
              </FieldLabel>
              ))}
          </RadioGroup>
          {!hasMembership && (
            <FieldDescription>
              Create a membership in channel settings to publish posts for
              members.
            </FieldDescription>
          )}
        </FieldSet>
        {["ppv", "members_ppv"].includes(policy) && (
          <Field>
            <FieldLabel htmlFor="post-amount">One-time price (USD)</FieldLabel>
            <Input
              id="post-amount"
              name="amount"
              inputMode="decimal"
              required
              pattern="[0-9]+(\.[0-9]{1,2})?"
              defaultValue={
                offer ? (Number(offer.unit_amount) / 1_000_000).toFixed(2) : ""
              }
              placeholder="9.00"
            />
            <FieldDescription>
              A price change creates new sale terms. Existing purchases remain
              valid.
            </FieldDescription>
          </Field>
        )}
        <FormError>{validation || save.error?.message || discard.error?.message}</FormError>
        {confirming ? (
          <div className="discard-confirm" role="alertdialog" aria-label="Discard this post?">
            <p>Discard this post? Its uploaded images and videos are deleted.</p>
            <Button type="button" variant="ghost" onClick={() => setConfirming?.(false)}>
              Keep editing
            </Button>
            <Button type="button" variant="destructive" disabled={discard.isPending} onClick={() => discard.mutate()}>
              {discard.isPending && <Spinner data-icon="inline-start" />}
              Discard
            </Button>
          </div>
        ) : (
          <Actions>
            <Button
              type="button"
              variant="ghost"
              onClick={() => (draft ? setConfirming?.(true) : onCancel())}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending || mediaBusy || needsText}>
              {(save.isPending || mediaBusy) && <Spinner data-icon="inline-start" />}
              {post ? "Save changes" : mediaBusy ? "Uploading…" : "Publish post"}
              <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
            </Button>
          </Actions>
        )}
      </FieldGroup>
    </form>
  );
}
const textRequired = "Add a title or some text, or attach an image or video.";
function FormActions({ children }: { children: ReactNode }) {
  return <div className="form-actions">{children}</div>;
}
