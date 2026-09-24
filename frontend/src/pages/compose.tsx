import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useBlocker, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { request } from "../api";
import type { Channel } from "../models";
import { useAuth } from "../session";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorState, Loading } from "../components/states";
import { PostForm } from "../components/post-editor";
import { channelPath, newChannelPath, postPath } from "../paths";
import { myChannelsKey, recallPostChannel, rememberPostChannel } from "../channels";

export function NewPostPage() {
  const auth = useAuth();
  const user = auth.user!.id;
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const location = useLocation();
  const channels = useQuery({
    queryKey: myChannelsKey(user),
    queryFn: () => request<{ data: Channel[] }>("/api/v1/me/channels"),
  });
  const list = channels.data?.data;
  const [picked, setPicked] = useState<string>();
  const [initial] = useState(() => ({
    slug: search.get("channel"),
    last: recallPostChannel(user),
  }));
  const current =
    list &&
    (list.find((c) => c.id === picked) ??
      list.find((c) => c.slug === initial.slug) ??
      list.find((c) => c.id === initial.last) ??
      list[0]);
  // Only a settled empty list sends the user to create a channel.
  const none = list?.length === 0;
  const settledNone = none && !channels.isFetching;
  useEffect(() => {
    if (settledNone) navigate(`${newChannelPath}?then=post`, { replace: true });
  }, [settledNone, navigate]);

  // A draft holds uploaded media; leaving asks before deleting it.
  const [hasDraft, setHasDraft] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const guard = useRef(false);
  useEffect(() => {
    guard.current = hasDraft;
    if (!hasDraft) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [hasDraft]);
  const blocker = useBlocker(() => guard.current);
  const leave = () => {
    if (location.key !== "default") navigate(-1);
    else navigate(current ? channelPath(current.slug) : "/");
  };

  if (channels.isPending || none) return <Loading />;
  if (channels.error || !current)
    return (
      <ErrorState
        error={channels.error || new Error("No channel found.")}
        retry={() => void channels.refetch()}
      />
    );
  return (
    <div className="page-narrow">
      <div className="page-heading">
        <h1>New post</h1>
        <p>
          Publishing to{" "}
          <Link to={channelPath(current.slug)} className="text-primary">
            {current.name}
          </Link>
          . Readers never become members of your editorial team.
        </p>
      </div>
      <Card>
        <CardContent>
          <PostForm
            channelID={current.id}
            hasMembership={current.membership.status !== "none"}
            channels={list}
            onChannel={(next) => {
              setPicked(next.id);
              rememberPostChannel(user, next.id);
            }}
            onDraft={setHasDraft}
            confirming={confirming || blocker.state === "blocked"}
            setConfirming={(value) => {
              setConfirming(value);
              if (!value && blocker.state === "blocked") blocker.reset();
            }}
            onDiscarded={() => {
              guard.current = false;
              setHasDraft(false);
              setConfirming(false);
              if (blocker.state === "blocked") blocker.proceed();
              else leave();
            }}
            onCancel={leave}
            onSaved={(saved) => {
              guard.current = false;
              rememberPostChannel(user, current.id);
              navigate(postPath(saved), { replace: true });
            }}
          />
        </CardContent>
      </Card>
    </div>
  );
}
