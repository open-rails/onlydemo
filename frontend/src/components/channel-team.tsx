import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { request } from "../api";
import type { Channel, Page } from "../models";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Field,
  Icon,
  Loading,
  Modal,
} from "./ui";
import { date } from "../format";

interface Member {
  subject_id: string;
  subject_kind: string;
  role: string;
}
interface Invite {
  id: string;
  role: string;
  expires_at?: string;
  redeemed_at?: string;
  revoked_at?: string;
}
export function ChannelTeam({ channel }: { channel: Channel }) {
  const root = `/auth/v1/channel/${encodeURIComponent(channel.slug)}`;
  const client = useQueryClient();
  const [error, setError] = useState("");
  const [remove, setRemove] = useState<Member | null>(null);
  const [invitation, setInvitation] = useState("");
  const [copied, setCopied] = useState(false);
  const members = useQuery({
    queryKey: ["team", channel.id],
    queryFn: () => request<Page<Member>>(`${root}/members`),
  });
  const invites = useQuery({
    queryKey: ["invites", channel.id],
    queryFn: () => request<Page<Invite>>(`${root}/invites/links`),
    enabled: channel.can_manage,
  });
  const mutate = useMutation({
    mutationFn: ({
      path,
      method,
      body,
    }: {
      path: string;
      method: string;
      body?: object;
    }) =>
      request(path, {
        method,
        ...(body ? { body: JSON.stringify(body) } : {}),
      }),
    onSuccess: async () => {
      setRemove(null);
      await client.invalidateQueries({ queryKey: ["team", channel.id] });
      await client.invalidateQueries({ queryKey: ["invites", channel.id] });
    },
  });
  const invite = useMutation({
    mutationFn: () =>
      request<{ code: string }>(`${root}/invites/links`, {
        method: "POST",
        body: JSON.stringify({ role: "editor", expires_in_seconds: 604800 }),
      }),
    onSuccess: (data) => {
      setInvitation(
        `${location.origin}/invite?code=${encodeURIComponent(data.code)}`,
      );
      setCopied(false);
      void invites.refetch();
    },
  });
  const add = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    const data = new FormData(event.currentTarget);
    mutate.mutate({
      path: `/api/v1/channels/${channel.id}/members`,
      method: "POST",
      body: {
        username: String(data.get("username")).trim(),
        role: String(data.get("role")),
      },
    });
  };
  return (
    <div className="stack">
      <div className="panel">
        <div className="panel-heading">
          <div>
            <h2>Editorial team</h2>
            <p>
              Owners and editors manage posts. Paying readers are never added
              here.
            </p>
          </div>
          <Icon name="users" />
        </div>
        {members.isPending ? (
          <Loading />
        ) : members.error ? (
          <ErrorState
            error={members.error}
            retry={() => void members.refetch()}
          />
        ) : (
          <div className="data-list">
            {members.data?.data.map((member) => (
              <div
                className="data-row"
                key={`${member.subject_id}:${member.role}`}
              >
                <span className="avatar">
                  <Icon name="user" size={16} />
                </span>
                <div className="data-row-main">
                  <h3 className="truncate">{member.subject_id}</h3>
                  <p>
                    {member.subject_kind === "user"
                      ? "Editorial member"
                      : "Application"}
                  </p>
                </div>
                <Badge tone="brand">{member.role}</Badge>
                {channel.can_manage && member.subject_kind === "user" && (
                  <div className="data-row-actions">
                    <Button
                      variant="secondary"
                      disabled={member.role === "owner"}
                      busy={mutate.isPending}
                      onClick={() =>
                        mutate.mutate({
                          path: `${root}/members/${encodeURIComponent(member.subject_id)}/roles/owner`,
                          method: "PUT",
                        })
                      }
                    >
                      {member.role === "owner" ? "Owner" : "Add owner role"}
                    </Button>
                    <Button variant="ghost" onClick={() => setRemove(member)}>
                      Remove
                    </Button>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
        {channel.can_manage && (
          <form className="stack" style={{ marginTop: 25 }} onSubmit={add}>
            <div className="input-row">
              <Field label="Existing username">
                <input name="username" required placeholder="their-username" />
              </Field>
              <Field label="Role">
                <select name="role" defaultValue="editor">
                  <option value="editor">Editor — manage posts</option>
                  <option value="owner">Owner — manage channel and team</option>
                </select>
              </Field>
            </div>
            <Button variant="secondary" type="submit" busy={mutate.isPending}>
              Add editorial member
            </Button>
          </form>
        )}
        {(mutate.error || error) && (
          <p className="form-error" role="alert">
            {mutate.error?.message || error}
          </p>
        )}
      </div>
      {channel.can_manage && (
        <div className="panel">
          <div className="panel-heading">
            <div>
              <h2>Editor invitations</h2>
              <p>
                Share a one-use invitation with an editor. Invitations expire
                after seven days.
              </p>
            </div>
          </div>
          <Button
            variant="secondary"
            busy={invite.isPending}
            onClick={() => invite.mutate()}
          >
            <Icon name="plus" size={16} />
            Create editor invite
          </Button>
          {invite.error && (
            <p className="form-error" role="alert">
              {invite.error.message}
            </p>
          )}
          {invitation && (
            <div className="stack" style={{ marginTop: 20 }}>
              <Field
                label="Private invitation link"
                hint="Only share this with the editor you intend to invite."
              >
                <input
                  value={invitation}
                  readOnly
                  onFocus={(event) => event.target.select()}
                />
              </Field>
              <Button
                variant="ghost"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(invitation);
                    setCopied(true);
                  } catch {
                    setError("Copy the selected link manually.");
                  }
                }}
              >
                {copied ? "Copied" : "Copy invitation link"}
              </Button>
            </div>
          )}
          {invites.error && <ErrorState error={invites.error} />}
          {invites.data && (
            <div className="data-list" style={{ marginTop: 25 }}>
              {invites.data.data.map((link) => (
                <div className="data-row" key={link.id}>
                  <div className="data-row-main">
                    <h3>Editor invitation</h3>
                    <p>Expires {date(link.expires_at)}</p>
                  </div>
                  <Badge>
                    {link.revoked_at
                      ? "Revoked"
                      : link.redeemed_at
                        ? "Accepted"
                        : "Pending"}
                  </Badge>
                  {!link.revoked_at && !link.redeemed_at && (
                    <Button
                      variant="ghost"
                      busy={mutate.isPending}
                      onClick={() =>
                        mutate.mutate({
                          path: `${root}/invites/links/${link.id}`,
                          method: "DELETE",
                        })
                      }
                    >
                      Revoke
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
      <Modal
        open={!!remove}
        onOpenChange={(open) => {
          if (!open) setRemove(null);
        }}
        title="Remove editorial access?"
        description="This removes their channel-management role. Their purchased reading access is separate."
      >
        <div className="form-actions">
          <Button variant="ghost" onClick={() => setRemove(null)}>
            Keep member
          </Button>
          <Button
            variant="danger"
            busy={mutate.isPending}
            onClick={() => {
              if (remove)
                mutate.mutate({
                  path: `/api/v1/channels/${channel.id}/members/${remove.subject_id}`,
                  method: "DELETE",
                });
            }}
          >
            Remove member
          </Button>
        </div>
        {mutate.error && <p className="form-error">{mutate.error.message}</p>}
      </Modal>
    </div>
  );
}
export function InvitePage() {
  const code = new URLSearchParams(location.search).get("code");
  const [accepted, setAccepted] = useState(false);
  const redeem = useMutation({
    mutationFn: () =>
      request("/auth/v1/invites/redeem", {
        method: "POST",
        body: JSON.stringify({ code }),
      }),
    onSuccess: () => setAccepted(true),
  });
  return (
    <div className="centered-page">
      <EmptyState
        icon="users"
        title={
          accepted
            ? "You joined the editorial team."
            : "An invitation to collaborate."
        }
        action={
          accepted ? (
            <a className="button button-primary" href="/me?tab=channels">
              Open my channels
            </a>
          ) : (
            <Button
              busy={redeem.isPending}
              disabled={!code}
              onClick={() => redeem.mutate()}
            >
              Accept editor invitation
            </Button>
          )
        }
      >
        {accepted
          ? "Your channel now appears under My channels."
          : "Accepting grants you editorial access to the inviting channel."}
      </EmptyState>
      {redeem.error && <ErrorState error={redeem.error} />}
    </div>
  );
}
