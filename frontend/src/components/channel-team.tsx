import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { request } from "../api";
import type { Channel, Page } from "../models";
import { HugeiconsIcon } from "@hugeicons/react";
import { Add01Icon, UserGroupIcon, UserIcon } from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
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
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import { EmptyState, ErrorState, FormError, Loading } from "./states";
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
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Editorial team</CardTitle>
          <CardDescription>
            Owners and editors manage posts. Paying readers are never added
            here.
          </CardDescription>
          <CardAction>
            <HugeiconsIcon icon={UserGroupIcon} />
          </CardAction>
        </CardHeader>
        <CardContent className="flex flex-col gap-6">
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
                  <HugeiconsIcon icon={UserIcon} size={16} />
                </span>
                <div className="data-row-main">
                  <h3 className="truncate">{member.subject_id}</h3>
                  <p>
                    {member.subject_kind === "user"
                      ? "Editorial member"
                      : "Application"}
                  </p>
                </div>
                <Badge>{member.role}</Badge>
                {channel.can_manage && member.subject_kind === "user" && (
                  <div className="data-row-actions">
                    <Button
                      variant="outline"
                      disabled={member.role === "owner" || mutate.isPending}
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
          <form onSubmit={add}>
            <FieldGroup>
              <div className="input-row">
                <Field>
                  <FieldLabel htmlFor="team-username">
                    Existing username
                  </FieldLabel>
                  <Input
                    id="team-username"
                    name="username"
                    required
                    placeholder="their-username"
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="team-role">Role</FieldLabel>
                  <NativeSelect id="team-role" name="role" defaultValue="editor">
                    <NativeSelectOption value="editor">
                      Editor — manage posts
                    </NativeSelectOption>
                    <NativeSelectOption value="owner">
                      Owner — manage channel and team
                    </NativeSelectOption>
                  </NativeSelect>
                </Field>
              </div>
              <Button
                variant="outline"
                type="submit"
                className="self-start"
                disabled={mutate.isPending}
              >
                {mutate.isPending && <Spinner data-icon="inline-start" />}
                Add editorial member
              </Button>
            </FieldGroup>
          </form>
        )}
        <FormError>{mutate.error?.message || error}</FormError>
        </CardContent>
      </Card>
      {channel.can_manage && (
        <Card>
          <CardHeader>
            <CardTitle>Editor invitations</CardTitle>
            <CardDescription>
              Share a one-use invitation with an editor. Invitations expire
              after seven days.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
          <Button
            variant="outline"
            className="self-start"
            disabled={invite.isPending}
            onClick={() => invite.mutate()}
          >
            {invite.isPending ? (
              <Spinner data-icon="inline-start" />
            ) : (
              <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
            )}
            Create editor invite
          </Button>
          <FormError>{invite.error?.message}</FormError>
          {invitation && (
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="invite-link">
                  Private invitation link
                </FieldLabel>
                <Input
                  id="invite-link"
                  value={invitation}
                  readOnly
                  onFocus={(event) => event.target.select()}
                />
                <FieldDescription>
                  Only share this with the editor you intend to invite.
                </FieldDescription>
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
            </FieldGroup>
          )}
          {invites.error && <ErrorState error={invites.error} />}
          {invites.data && (
            <div className="data-list">
              {invites.data.data.map((link) => (
                <div className="data-row" key={link.id}>
                  <div className="data-row-main">
                    <h3>Editor invitation</h3>
                    <p>Expires {date(link.expires_at)}</p>
                  </div>
                  <Badge variant="secondary">
                    {link.revoked_at
                      ? "Revoked"
                      : link.redeemed_at
                        ? "Accepted"
                        : "Pending"}
                  </Badge>
                  {!link.revoked_at && !link.redeemed_at && (
                    <Button
                      variant="ghost"
                      disabled={mutate.isPending}
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
          </CardContent>
        </Card>
      )}
      <Dialog
        open={!!remove}
        onOpenChange={(open) => {
          if (!open) setRemove(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove editorial access?</DialogTitle>
            <DialogDescription>
              This removes their channel-management role. Their purchased
              reading access is separate.
            </DialogDescription>
          </DialogHeader>
          <FormError>{mutate.error?.message}</FormError>
          <DialogFooter>
          <Button variant="ghost" onClick={() => setRemove(null)}>
            Keep member
          </Button>
          <Button
            variant="destructive"
            disabled={mutate.isPending}
            onClick={() => {
              if (remove)
                mutate.mutate({
                  path: `/api/v1/channels/${channel.id}/members/${remove.subject_id}`,
                  method: "DELETE",
                });
            }}
          >
            {mutate.isPending && <Spinner data-icon="inline-start" />}
            Remove member
          </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
        icon={UserGroupIcon}
        title={
          accepted
            ? "You joined the editorial team."
            : "An invitation to collaborate."
        }
        action={
          accepted ? (
            <Button nativeButton={false} render={<a href="/me?tab=channels" />}>
              Open my channels
            </Button>
          ) : (
            <Button
              disabled={!code || redeem.isPending}
              onClick={() => redeem.mutate()}
            >
              {redeem.isPending && <Spinner data-icon="inline-start" />}
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
