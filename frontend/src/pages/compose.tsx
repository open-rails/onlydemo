import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { ContentEditor } from "@openrails/contentkit-ui";
import { Field, FieldLabel } from "@/components/ui/field";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { ErrorState, Loading } from "../components/states";
import { useAuth } from "../session";
import { recallPostChannel, rememberPostChannel, useMyChannels } from "../roles";
import { channelPath, editPostPath, newChannelPath } from "../paths";

// A new post: ContentKit's editor, the post owned by the chosen channel.
export function NewPostPage() {
  const user = useAuth().user!.id;
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const channels = useMyChannels();
  const list = channels.data;
  const [picked, setPicked] = useState<string>();
  const [initial] = useState(() => ({ slug: search.get("channel"), last: recallPostChannel(user) }));
  const current =
    list &&
    (list.find((c) => c.group.id === picked) ?? list.find((c) => c.group.slug === initial.slug) ?? list.find((c) => c.group.id === initial.last) ?? list[0]);
  const none = list?.length === 0;
  useEffect(() => {
    if (none) navigate(`${newChannelPath}?then=post`, { replace: true });
  }, [none, navigate]);
  if (channels.isPending || none) return <Loading />;
  if (channels.error || !current) return <ErrorState error={channels.error || new Error("No channel found.")} retry={() => void channels.refetch()} />;
  return (
    <div className="page-narrow">
      <div className="page-heading">
        <h1>New post</h1>
        <p>
          Publishing to{" "}
          <Link to={channelPath(current.group)} className="text-primary">
            {current.group.name}
          </Link>
          . Readers never become members of your editorial team.
        </p>
      </div>
      {list.length > 1 && (
        <Field className="mb-4">
          <FieldLabel htmlFor="post-channel">Channel</FieldLabel>
          <NativeSelect
            id="post-channel"
            className="w-full"
            value={current.group.id}
            onChange={(event) => {
              setPicked(event.target.value);
              rememberPostChannel(user, event.target.value);
            }}
          >
            {list.map(({ group }) => (
              <NativeSelectOption key={group.id} value={group.id}>
                {group.name} (@{group.slug})
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      )}
      <ContentEditor
        key={current.group.id}
        id={null}
        kind="post"
        ownerGroup={current.group.id}
        onCreated={(created) => {
          rememberPostChannel(user, current.group.id);
          navigate(editPostPath(created.id), { replace: true });
        }}
      />
    </div>
  );
}
