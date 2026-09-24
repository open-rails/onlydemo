import { HugeiconsIcon } from "@hugeicons/react";
import { SquareLock02Icon, StarIcon } from "@hugeicons/core-free-icons";
import { cn } from "cn";
import { Badge } from "@/components/ui/badge";
import { money } from "../format";
import type { AccessPolicy, Offer } from "../models";

const premium =
  "bg-amber-500/15 text-amber-700 dark:bg-amber-400/15 dark:text-amber-300";

export function MemberStar({ className }: { className?: string }) {
  return (
    <HugeiconsIcon
      icon={StarIcon}
      fill="currentColor"
      data-icon="inline-start"
      className={className}
      aria-hidden
    />
  );
}

// PolicyBadge marks premium posts: a star for membership posts, star + price
// for members-only purchases, a lock + price for one-time purchases.
export function PolicyBadge({
  policy,
  offer,
  className,
}: {
  policy: AccessPolicy;
  offer?: Offer;
  className?: string;
}) {
  const price = offer ? money(offer.unit_amount, offer.currency) : "";
  if (policy === "membership")
    return (
      <Badge
        className={cn(premium, className)}
        title="Included with membership"
        aria-label="Included with membership"
      >
        <MemberStar />
        Members
      </Badge>
    );
  if (policy === "members_ppv")
    return (
      <Badge
        className={cn(premium, className)}
        title="Members-only purchase"
        aria-label={`Members-only purchase${price ? ` for ${price}` : ""}`}
      >
        <MemberStar />
        Members{price && ` · ${price}`}
      </Badge>
    );
  if (policy === "ppv")
    return (
      <Badge
        variant="secondary"
        className={className}
        title="One-time purchase"
        aria-label={`One-time purchase${price ? ` for ${price}` : ""}`}
      >
        <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
        {price || "Purchase"}
      </Badge>
    );
  return null;
}
