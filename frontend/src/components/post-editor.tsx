import { useState, type FormEvent } from "react";
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
      detail: "Current and future members get access while subscribed.",
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
export function PostEditor({
  channelID,
  post,
  open,
  onClose,
}: {
  channelID: string;
  post?: Post;
  open: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value) onClose();
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
          post={post}
          onClose={onClose}
        />
        )}
      </DialogContent>
    </Dialog>
  );
}
function EditorForm({
  channelID,
  post,
  onClose,
}: {
  channelID: string;
  post?: Post;
  onClose: () => void;
}) {
  const client = useQueryClient();
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
    onSuccess: async () => {
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
        channel_id: channelID,
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
              pattern="[a-z0-9-]+"
              defaultValue={post?.slug}
              placeholder="a-short-slug"
            />
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
        <FieldSet>
          <FieldLegend variant="label">Who can read this?</FieldLegend>
          <RadioGroup
            className="grid gap-3 sm:grid-cols-2"
            value={policy}
            onValueChange={(value) => setPolicy(value as AccessPolicy)}
          >
            {policies.map((choice) => (
              <FieldLabel htmlFor={`policy-${choice.value}`} key={choice.value}>
                <Field orientation="horizontal">
                  <FieldContent>
                    <FieldTitle>{choice.title}</FieldTitle>
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
        <FormError>{validation || save.error?.message}</FormError>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            {post ? "Save changes" : "Publish post"}
            <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
          </Button>
        </DialogFooter>
      </FieldGroup>
    </form>
  );
}
