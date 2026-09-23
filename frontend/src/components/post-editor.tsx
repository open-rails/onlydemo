import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { request } from "../api";
import type { AccessPolicy, Post } from "../models";
import { micros } from "../format";
import { Button, Field, Icon, Modal } from "./ui";

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
    <Modal
      open={open}
      onOpenChange={(value) => {
        if (!value) onClose();
      }}
      title={post ? "Edit post" : "Write a new post"}
      description="Publish to your channel. Readers never become members of your editorial team."
      wide
    >
      {open && (
        <EditorForm
          key={post?.id || "new"}
          channelID={channelID}
          post={post}
          onClose={onClose}
        />
      )}
    </Modal>
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
    <form className="stack" onSubmit={submit}>
      <div className="input-row">
        <Field label="Title">
          <input
            name="title"
            required
            maxLength={200}
            autoFocus
            defaultValue={post?.title}
          />
        </Field>
        <Field label="Slug">
          <input
            name="slug"
            required
            pattern="[a-z0-9-]+"
            defaultValue={post?.slug}
            placeholder="a-short-slug"
          />
        </Field>
      </div>
      <Field label="Story">
        <textarea
          name="body"
          className="editor-body"
          required
          defaultValue={post?.body}
          placeholder="Start writing…"
        />
      </Field>
      <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
        <legend className="field">Who can read this?</legend>
        <div className="choice-grid">
          {policies.map((choice) => (
            <label className="choice" key={choice.value}>
              <input
                type="radio"
                name="access_policy"
                value={choice.value}
                checked={policy === choice.value}
                onChange={() => setPolicy(choice.value)}
              />
              <span>
                <strong>{choice.title}</strong>
                <small>{choice.detail}</small>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      {["ppv", "members_ppv"].includes(policy) && (
        <Field
          label="One-time price (USD)"
          hint="A price change creates new sale terms. Existing purchases remain valid."
        >
          <input
            name="amount"
            inputMode="decimal"
            required
            pattern="[0-9]+(\.[0-9]{1,2})?"
            defaultValue={
              offer ? (Number(offer.unit_amount) / 1_000_000).toFixed(2) : ""
            }
            placeholder="9.00"
          />
        </Field>
      )}
      {(validation || save.error) && (
        <p className="form-error" role="alert">
          {validation || save.error?.message}
        </p>
      )}
      <div className="form-actions">
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" busy={save.isPending}>
          {post ? "Save changes" : "Publish post"}
          <Icon name="arrow" size={16} />
        </Button>
      </div>
    </form>
  );
}
