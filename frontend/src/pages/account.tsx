import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountSecurity } from "@openrails/auth-ui";
import { AccountBilling } from "@openrails/billing-ui";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Add01Icon,
  Book02Icon,
  Settings02Icon,
  Tick02Icon,
  Wallet01Icon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { request } from "../api";
import type { AccountData, AppConfig } from "../models";
import {
  EmptyState,
  ErrorState,
  FormError,
  Loading,
} from "../components/states";
import { PostCard, ChannelCard } from "../components/cards";
import { useAuth } from "../session";

const titles: Record<string, string> = {
  library: "Purchased",
  billing: "Billing",
  channels: "My channels",
  settings: "Account",
};
export function AccountPage() {
  const [search, setSearch] = useSearchParams();
  const tab = search.get("tab") || "library";
  const auth = useAuth();
  const summary = useInfiniteQuery({
    queryKey: ["me-dashboard", auth.user?.id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      request<AccountData & { has_more?: boolean; next_cursor?: string }>(
        `/api/v1/me${pageParam ? `?before=${encodeURIComponent(pageParam)}` : ""}`,
      ),
    getNextPageParam: (last) =>
      last.has_more && last.next_cursor ? last.next_cursor : undefined,
  });
  const current = summary.data?.pages[0];
  return (
    <>
      <div className="page-title">
        <h1>{titles[tab] || "Account"}</h1>
        <span className="muted">@{auth.user?.username}</span>
      </div>
      <Tabs
        value={tab}
        onValueChange={(value) => setSearch({ tab: String(value) })}
        className="my-4"
      >
        <TabsList variant="line" aria-label="Account sections" className="w-full overflow-x-auto">
          {Object.entries(titles).map(([key, label]) => (
            <TabsTrigger key={key} value={key}>
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      {search.get("setup_id") && <SetupReturn id={search.get("setup_id")!} />}
      {summary.error && (
        <ErrorState
          error={summary.error}
          retry={() => void summary.refetch()}
        />
      )}
      {summary.isPending ? (
        <Loading />
      ) : summary.error ? null : (
        <>
          {tab === "library" && (
            <>
              <Library
                posts={
                  summary.data?.pages.flatMap((page) => page.purchased_posts) ||
                  []
                }
                searching={!!summary.hasNextPage}
              />
              {summary.hasNextPage && (
                <div className="pagination">
                  <Button
                    variant="outline"
                    disabled={summary.isFetchingNextPage}
                    onClick={() => void summary.fetchNextPage()}
                  >
                    {summary.isFetchingNextPage && (
                      <Spinner data-icon="inline-start" />
                    )}
                    Look for older purchases
                  </Button>
                </div>
              )}
            </>
          )}
          {tab === "channels" && (
            <>
              <div className="section-heading">
                <div>
                  <h2>Channels you run</h2>
                  <p>
                    Only channels where you can manage or edit publication
                    appear here.
                  </p>
                </div>
                <Button nativeButton={false} render={<Link to="/channels/new" />}>
                  <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
                  Create channel
                </Button>
              </div>
              {current?.manageable_channels?.length ? (
                <div className="channel-grid">
                  {current.manageable_channels.map((channel) => (
                    <ChannelCard channel={channel} key={channel.id} />
                  ))}
                </div>
              ) : (
                <EmptyState
                  icon={Settings02Icon}
                  title="No editorial channels yet."
                >
                  Create a channel or accept an editor invitation to start
                  publishing.
                </EmptyState>
              )}
            </>
          )}
          {tab === "billing" && <Billing />}
          {tab === "settings" && <AccountSettings />}
        </>
      )}
    </>
  );
}
function Library({
  posts,
  searching,
}: {
  posts: AccountData["purchased_posts"];
  searching: boolean;
}) {
  const unique = [...new Map(posts.map((post) => [post.id, post])).values()];
  return unique.length ? (
    <div className="feed">
      {unique.map((post) => (
        <PostCard post={{ ...post, purchased: true }} key={post.id} />
      ))}
    </div>
  ) : (
    <EmptyState
      icon={Book02Icon}
      title={
        searching ? "No purchases on this page." : "Nothing unlocked yet."
      }
      action={
        !searching ? (
          <Button nativeButton={false} render={<Link to="/channels" />}>
            Explore creators
          </Button>
        ) : undefined
      }
    >
      {searching
        ? "Search older posts below to find earlier purchases."
        : "Purchased posts appear here while their content is available. Payment history is retained separately."}
    </EmptyState>
  );
}
function Billing() {
  const config = useQuery({
    queryKey: ["config"],
    queryFn: () => request<AppConfig>("/api/v1/config", {}, false),
  });
  const nmi = config.data?.psps.find((psp) => psp.rail === "nmi");
  const key = nmi?.config?.tokenization_key;
  const url = nmi?.config?.tokenization_url;
  const cardSetup =
    nmi && key && url && !key.startsWith("preview_")
      ? { provider: nmi.key, tokenizationKey: key, tokenizationURL: url }
      : undefined;
  if (config.isPending) return <Loading />;
  return (
    <AccountBilling
      plansHref="/channels"
      cardSetup={cardSetup}
      defaultCurrency="USD"
    />
  );
}
function SetupReturn({ id }: { id: string }) {
  const confirm = useMutation({
    mutationFn: () =>
      request<{ payment_method_id?: string; status: string }>(
        `/billing/v1/me/payment-methods/stripe-setup/${encodeURIComponent(id)}/confirm`,
        { method: "POST" },
      ),
  });
  const setup = useQuery({
    queryKey: ["setup-return", id],
    queryFn: () =>
      request<{ payment_method_id?: string; status: string }>(
        `/billing/v1/me/payment-methods/stripe-setup/${encodeURIComponent(id)}`,
      ),
  });
  const done = !!(
    setup.data?.payment_method_id || confirm.data?.payment_method_id
  );
  return (
    <Alert className="mb-6">
      <HugeiconsIcon
        icon={done ? Tick02Icon : Wallet01Icon}
        className={done ? "text-success" : undefined}
      />
      <AlertTitle>
        {done ? "Card setup verified." : "Finish card verification"}
      </AlertTitle>
      <AlertDescription>
        <p>
          {done
            ? "Return to the channel to review and start your membership."
            : "Confirm the setup with the server after returning from Stripe. This does not start a membership."}
        </p>
        {!done && (
          <Button disabled={confirm.isPending} onClick={() => confirm.mutate()}>
            {confirm.isPending && <Spinner data-icon="inline-start" />}
            Verify saved card
          </Button>
        )}
        <FormError>{setup.error?.message || confirm.error?.message}</FormError>
      </AlertDescription>
    </Alert>
  );
}
function AccountSettings() {
  const navigate = useNavigate();
  return <AccountSecurity onDeleted={() => navigate("/")} />;
}
