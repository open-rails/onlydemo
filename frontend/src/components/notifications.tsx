import { Link } from "react-router-dom";
import { NotificationBell, NotificationInbox } from "@openrails/contentkit-ui";
import type { NotificationItem } from "@openrails/contentkit-ui/client";

const titles: Record<string, string> = {
  "comment.reply": "Someone replied to your comment",
  "comment.liked": "Someone liked your comment",
  premium_started: "Subscription started",
  premium_renewed: "Subscription renewed",
  premium_ended: "Subscription ended",
  payment_method_failed: "Payment failed",
  payment_method_auto_updated: "Payment method updated",
  payment_method_update_required: "Update your payment method",
  payment_authentication_required: "Confirm your payment",
  one_off_purchase_completed: "Purchase completed",
  order_collection_stopped: "Payment collection stopped",
  account_delinquent: "You have overdue payments",
  account_delinquency_cleared: "Your account is up to date",
  order_due: "Payment due",
  order_overdue: "Payment overdue",
  subscription_reprice_scheduled: "Subscription price change scheduled",
  subscription_plan_change_scheduled: "Subscription plan change scheduled",
  subscription_changed: "Subscription updated",
};

function NotificationContent({ item }: { item: NotificationItem }) {
  const words = item.kind.replace(/[_.-]+/g, " ").trim();
  const title = titles[item.kind] ?? (words ? words[0].toUpperCase() + words.slice(1) : "New update");
  const data = item.data as Record<string, unknown> | undefined;
  const message = typeof data?.message === "string" ? data.message : undefined;
  return <div className="grid gap-2">
    <strong>{title}</strong>
    {message && <p>{message}</p>}
    {item.action_requested && <p className="text-sm text-muted-foreground">{item.origin.system === "openrails:onlydemo" ? "Action was requested. Open billing to check its current status." : "Action requested"}</p>}
  </div>;
}

function NotificationActions({ item }: { item: NotificationItem }) {
  return item.origin.system === "openrails:onlydemo"
    ? <Link className="text-sm font-medium text-primary underline underline-offset-4" to="/me?tab=billing">View billing</Link>
    : null;
}

const renderItem = (item: NotificationItem) => <NotificationContent item={item} />;
const renderActions = (item: NotificationItem) => <NotificationActions item={item} />;

export function InboxBell() {
  return <NotificationBell renderItem={renderItem} renderActions={renderActions} />;
}

export function AccountNotifications() {
  return <NotificationInbox renderItem={renderItem} renderActions={renderActions} refreshInterval={30_000} />;
}
