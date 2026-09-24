import { useCallback, useRef, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { request } from "../api";
import type { AccessPolicy, Post } from "../models";
import { micros } from "../format";
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
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { FormError } from "./states";
import { MemberStar } from "./policy-badge";
import { DraftMediaEditor, MediaDrop, type DraftMediaHandle } from "./post-media";
import { screenFiles } from "../media";

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
export function PostEditor({
  channelID,
  hasMembership,
  post,
  open,
  onClose,
  onSaved,
}: {
  channelID: string;
  hasMembership: boolean;
  post?: Post;
  open: boolean;
  onClose: () => void;
  onSaved?: (post: Post) => void;
}) {
  // Closing a composer that holds a draft asks first: it deletes the draft.
  const [hasDraft, setHasDraft] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const requestClose = () => (hasDraft ? setConfirming(true) : onClose());
  const close = () => {
    setHasDraft(false);
    setConfirming(false);
    onClose();
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value) requestClose();
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{post ? "Edit post" : "Write a new post"}</DialogTitle>
          <DialogDescription>
            Publish to your channel. Readers never become members of your
            editorial team.
          </DialogDescription>
        </DialogHeader>
        {open && (
        <EditorForm
          key={post?.id || "new"}
          channelID={channelID}
          hasMembership={hasMembership}
          post={post}
          onClose={close}
          onSaved={onSaved}
          onDraft={setHasDraft}
          requestClose={requestClose}
          confirming={confirming}
          setConfirming={setConfirming}
        />
        )}
      </DialogContent>
    </Dialog>
  );
}
function EditorForm({
  channelID,
  hasMembership,
  post,
  onClose,
  onSaved,
  onDraft,
  requestClose,
  confirming,
  setConfirming,
}: {
  channelID: string;
  hasMembership: boolean;
  post?: Post;
  onClose: () => void;
  onSaved?: (post: Post) => void;
  onDraft: (has: boolean) => void;
  requestClose: () => void;
  confirming: boolean;
  setConfirming: (v: boolean) => void;
}) {
  const client = useQueryClient();
  // New posts upload into a draft created with the first file; Publish turns
  // it into the post, Cancel deletes it with its media.
  const [draft, setDraft] = useState<{ id: number; files: File[] }>();
  const [mediaBusy, setMediaBusy] = useState(false);
  const [refused, setRefused] = useState<string[]>([]);
  const media = useRef<DraftMediaHandle>(null);
  const onBusy = useCallback((busy: boolean) => setMediaBusy(busy), []);
  const createDraft = useMutation({
    mutationFn: () =>
      request<{ id: number }>("/api/v1/posts", {
        method: "POST",
        body: JSON.stringify({ channel_id: channelID, draft: true }),
      }),
  });
  const addFirst = (list: File[]) => {
    const { accepted, refused } = screenFiles(list, { files: 0, videos: 0 });
    setRefused(refused);
    if (accepted.length === 0) return;
    createDraft.mutate(undefined, {
      onSuccess: ({ id }) => {
        setDraft({ id, files: accepted });
        onDraft(true);
      },
    });
  };
  const discard = useMutation({
    mutationFn: async () => {
      media.current?.discard();
      if (draft) await request(`/api/v1/posts/${draft.id}`, { method: "DELETE" });
    },
    onSuccess: onClose,
  });

  const [policy, setPolicy] = useState<AccessPolicy>(
    post?.access_policy || "public",
  );
  const [validation, setValidation] = useState("");
  const offer = post?.offers?.find((value) => !value.auto_renew);
  const save = useMutation({
    mutationFn: (body: object) =>
      request<Post>(post ? `/api/v1/posts/${post.id}` : "/api/v1/posts", {
        method: post ? "PATCH" : "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: async (saved) => {
      // Follow a renamed slug (or open the new post) before refetching.
      onSaved?.(saved);
      await client.invalidateQueries();
      onClose();
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setValidation("");
    const data = new FormData(event.currentTarget);
    try {
      save.mutate({
        ...(post ? {} : { channel_id: channelID }),
        ...(draft ? { draft_id: draft.id } : {}),
        title: String(data.get("title")).trim(),
        slug: String(data.get("slug")).trim(),
        body: String(data.get("body")),
        access_policy: policy,
        ...(["ppv", "members_ppv"].includes(policy)
          ? {
              price: {
                unit_amount: micros(String(data.get("amount"))),
                currency: "USD",
              },
            }
          : {}),
      });
    } catch (error) {
      setValidation(
        error instanceof Error ? error.message : "Please check your fields.",
      );
    }
  };
  return (
    <form onSubmit={submit}>
      <FieldGroup>
        <div className="input-row">
          <Field>
            <FieldLabel htmlFor="post-title">Title</FieldLabel>
            <Input
              id="post-title"
              name="title"
              required
              maxLength={200}
              autoFocus
              defaultValue={post?.title}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="post-slug">Slug</FieldLabel>
            <Input
              id="post-slug"
              name="slug"
              required
              pattern="[a-z0-9]+(-[a-z0-9]+)*"
              maxLength={120}
              defaultValue={post?.slug}
              placeholder="a-short-slug"
            />
            <FieldDescription>
              Unique within this channel. Lowercase letters, numbers, and
              dashes.
            </FieldDescription>
          </Field>
        </div>
        <Field>
          <FieldLabel htmlFor="post-body">Story</FieldLabel>
          <Textarea
            id="post-body"
            name="body"
            className="min-h-48"
            required
            defaultValue={post?.body}
            placeholder="Start writing…"
          />
        </Field>
        {!post &&
          (draft ? (
            <DraftMediaEditor key={draft.id} postID={draft.id} initial={draft.files} handle={media} onBusy={onBusy} />
          ) : (
            <Field>
              <FieldLabel>Images and videos</FieldLabel>
              <MediaDrop empty onFiles={addFirst} disabled={createDraft.isPending} />
              {createDraft.isPending && (
                <FieldDescription>
                  <Spinner className="inline" /> Preparing uploads…
                </FieldDescription>
              )}
              {refused.length > 0 && (
                <ul className="media-refused" role="alert">
                  {refused.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </ul>
              )}
              <FormError>{createDraft.error?.message}</FormError>
            </Field>
          ))}
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
            <Button type="button" variant="ghost" onClick={() => setConfirming(false)}>
              Keep editing
            </Button>
            <Button type="button" variant="destructive" disabled={discard.isPending} onClick={() => discard.mutate()}>
              {discard.isPending && <Spinner data-icon="inline-start" />}
              Discard
            </Button>
          </div>
        ) : (
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={requestClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending || mediaBusy || createDraft.isPending}>
              {(save.isPending || mediaBusy) && <Spinner data-icon="inline-start" />}
              {post ? "Save changes" : mediaBusy ? "Uploading…" : "Publish post"}
              <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
            </Button>
          </DialogFooter>
        )}
      </FieldGroup>
    </form>
  );
}
