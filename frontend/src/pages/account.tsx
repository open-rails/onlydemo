import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountSecurity } from "@openrails/auth-ui";
import { AccountBilling } from "@openrails/billing-ui";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Add01Icon,
  Book02Icon,
  Settings02Icon,
} from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { request } from "../api";
import { AccountBillingScope } from "../billing";
import type { AccountData, AppConfig } from "../models";
import {
  EmptyState,
  ErrorState,
  Loading,
} from "../components/states";
import { AvatarUpload } from "@openrails/contentkit-ui";
import { Card, CardContent } from "@/components/ui/card";
import { UserAvatar, ChannelCard } from "../components/cards";
import { PostView } from "../components/post-view";
import { userRef } from "../media";
import { useAuth } from "../session";
import { channelsPath, newChannelPath } from "../paths";
import { VerifyEmailBanner } from "../verify";
import { AccountNotifications } from "../components/notifications";

const titles: Record<string, string> = {
  library: "Purchased",
  billing: "Billing",
  notifications: "Notifications",
  channels: "My channels",
  settings: "Account",
};
export function AccountPage() {
  const [search, setSearch] = useSearchParams();
  const tab = search.get("tab") || "library";
  const auth = useAuth();
  const summary = useInfiniteQuery({
    queryKey: ["me-dashboard", auth.user?.id],
    enabled: tab !== "notifications",
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
        {current && (
          <Link to="/me?tab=settings" aria-label="Change your avatar">
            <UserAvatar user={current.user} className="avatar-lg" />
          </Link>
        )}
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
      {tab !== "notifications" && summary.error && (
        <ErrorState
          error={summary.error}
          retry={() => void summary.refetch()}
        />
      )}
      {tab === "notifications" ? (
        <AccountNotifications />
      ) : summary.isPending ? (
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
                <Button nativeButton={false} render={<Link to={newChannelPath} />}>
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
          {tab === "settings" && current && <AccountSettings user={current.user} />}
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
        <PostView post={{ ...post, purchased: true }} key={post.id} />
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
          <Button nativeButton={false} render={<Link to={channelsPath} />}>
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
  if (config.isPending) return <Loading />;
  return (
    <AccountBillingScope>
      <AccountBilling
        plansHref={channelsPath}
        defaultCurrency="USD"
        defaultCountry={config.data?.country || undefined}
      />
    </AccountBillingScope>
  );
}
function AccountSettings({ user }: { user: AccountData["user"] }) {
  const navigate = useNavigate();
  return (
    <>
      <VerifyEmailBanner />
      <Card className="mb-6">
        <CardContent>
          <AvatarUpload
            item={userRef(user.id)}
            image={user.avatar}
            label="Profile photo"
          />
        </CardContent>
      </Card>
      <AccountSecurity onDeleted={() => navigate("/")} />
    </>
  );
}
