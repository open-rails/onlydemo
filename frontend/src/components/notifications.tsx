import { NotificationBell, NotificationInbox } from "@openrails/auth-ui";
import type { NotificationItem } from "@openrails/auth-ui/client";
import { useSession } from "@openrails/auth-ui/react";
import { BillingNotification, isBillingNotification } from "@openrails/billing-ui";

const titles: Record<string, string> = {
  "comment.reply": "Someone replied to your comment",
  "comment.liked": "Someone liked your comment",
};

function renderItem(item: NotificationItem) {
  if (isBillingNotification(item)) return <BillingNotification
    item={item}
    appearance={{ theme: "inherit" }}
    actions={{
      update_payment_method: "/me?tab=billing",
      authenticate_payment: "/me?tab=billing",
      manage_billing: "/me?tab=billing",
      resubscribe: "/me?tab=billing",
    }}
  />;
  return titles[item.kind] ? <p>{titles[item.kind]}</p> : null;
}

function useViewer() {
  const session = useSession();
  return session.status === "authenticated" ? session.userId : null;
}

export function InboxBell() {
  const viewer = useViewer();
  return viewer ? <NotificationBell key={viewer} renderItem={renderItem} /> : null;
}

export function AccountNotifications() {
  const viewer = useViewer();
  return viewer ? <NotificationInbox key={viewer} renderItem={renderItem} /> : null;
}
