import { useQuery } from "@tanstack/react-query";
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
import type { AppConfig } from "../models";
import { useLibrary, useMyChannels } from "../content";
import { Badge } from "@/components/ui/badge";
import { usePublicImage } from "@openrails/contentkit-ui/react";
import {
  EmptyState,
  ErrorState,
  Loading,
} from "../components/states";
import { AvatarUpload } from "@openrails/contentkit-ui";
import { Card, CardContent } from "@/components/ui/card";
import { UserAvatar, ChannelCard } from "../components/cards";
import { userRef } from "../media";
import { useAuth } from "../session";
import { channelsPath, newChannelPath, postByIDPath } from "../paths";
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
  const user = auth.user!;
  return (
    <>
      <div className="page-title">
        <Link to="/me?tab=settings" aria-label="Change your avatar">
          <UserAvatar user={user} className="avatar-lg" />
        </Link>
        <h1>{titles[tab] || "Account"}</h1>
        <span className="muted">@{user.username}</span>
      </div>
      <Tabs value={tab} onValueChange={(value) => setSearch({ tab: String(value) })} className="my-4">
        <TabsList variant="line" aria-label="Account sections" className="w-full overflow-x-auto">
          {Object.entries(titles).map(([key, label]) => (
            <TabsTrigger key={key} value={key}>
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      {tab === "notifications" && <AccountNotifications />}
      {tab === "library" && <Library />}
      {tab === "channels" && <MyChannels />}
      {tab === "billing" && <Billing />}
      {tab === "settings" && <AccountSettings user={user} />}
    </>
  );
}
// The posts the viewer bought, from ContentKit's library: deleted ones stay.
function Library() {
  const library = useLibrary();
  const items = library.data?.pages.flatMap((page) => page.items) ?? [];
  if (library.isPending) return <Loading />;
  if (library.error) return <ErrorState error={library.error} retry={() => void library.refetch()} />;
  if (!items.length)
    return (
      <EmptyState
        icon={Book02Icon}
        title="Nothing unlocked yet."
        action={
          <Button nativeButton={false} render={<Link to={channelsPath} />}>
            Explore creators
          </Button>
        }
      >
        Posts you buy appear here, even after their creator deletes them. Payment history is retained separately.
      </EmptyState>
    );
  return (
    <>
      <ul className="library-list">
        {items.map((item) => (
          <li key={item.content_id}>
            <Link to={postByIDPath(item.content_id)}>{item.title || "Untitled post"}</Link>
            {item.deleted && <Badge variant="outline">Deleted by its creator</Badge>}
          </li>
        ))}
      </ul>
      {library.hasNextPage && (
        <div className="pagination">
          <Button variant="outline" disabled={library.isFetchingNextPage} onClick={() => void library.fetchNextPage()}>
            {library.isFetchingNextPage && <Spinner data-icon="inline-start" />}
            Older purchases
          </Button>
        </div>
      )}
    </>
  );
}
function MyChannels() {
  const channels = useMyChannels();
  return (
    <>
      <div className="section-heading">
        <div>
          <h2>Channels you run</h2>
          <p>Only channels where you can manage or edit publication appear here.</p>
        </div>
        <Button nativeButton={false} render={<Link to={newChannelPath} />}>
          <HugeiconsIcon icon={Add01Icon} data-icon="inline-start" />
          Create channel
        </Button>
      </div>
      {channels.isPending ? (
        <Loading />
      ) : channels.error ? (
        <ErrorState error={channels.error} retry={() => void channels.refetch()} />
      ) : channels.data?.data.length ? (
        <div className="channel-grid">
          {channels.data.data.map((channel) => (
            <ChannelCard channel={channel} key={channel.id} />
          ))}
        </div>
      ) : (
        <EmptyState icon={Settings02Icon} title="No editorial channels yet.">
          Create a channel or accept an editor invitation to start publishing.
        </EmptyState>
      )}
    </>
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
function AccountSettings({ user }: { user: { id: string; username: string } }) {
  const navigate = useNavigate();
  const avatar = usePublicImage("user", user.id, "avatar");
  return (
    <>
      <VerifyEmailBanner />
      <Card className="mb-6">
        <CardContent>
          <AvatarUpload
            item={userRef(user.id)}
            image={avatar.isDefault ? null : avatar.image}
            label="Profile photo"
          />
        </CardContent>
      </Card>
      <AccountSecurity onDeleted={() => navigate("/")} />
    </>
  );
}
