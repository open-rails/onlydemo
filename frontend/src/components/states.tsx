import type { ReactNode } from "react";
import { HugeiconsIcon, type IconSvgElement } from "@hugeicons/react";
import { Alert02Icon, Book02Icon } from "@hugeicons/core-free-icons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";

export function EmptyState({
  title,
  children,
  action,
  icon = Book02Icon,
}: {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  icon?: IconSvgElement;
}) {
  return (
    <Empty className="border bg-card">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <HugeiconsIcon icon={icon} />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {children && <EmptyDescription>{children}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  );
}

export function ErrorState({
  error,
  retry,
}: {
  error: unknown;
  retry?: () => void;
}) {
  return (
    <Alert variant="destructive">
      <HugeiconsIcon icon={Alert02Icon} />
      <AlertTitle>We couldn’t load this.</AlertTitle>
      <AlertDescription>
        <p>
          {error instanceof Error
            ? error.message
            : "Something went wrong. Please try again."}
        </p>
        {retry && (
          <Button variant="outline" size="sm" onClick={retry}>
            Try again
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}

export function FormError({ children }: { children?: ReactNode }) {
  if (!children) return null;
  return (
    <p className="text-sm text-destructive" role="alert">
      {children}
    </p>
  );
}

export function Loading({
  cards = false,
  label = "Loading…",
}: {
  cards?: boolean;
  label?: string;
}) {
  if (cards)
    return (
      <div className="card-grid" aria-label="Loading posts" role="status">
        {[0, 1, 2].map((n) => (
          <div className="flex flex-col gap-3 rounded-2xl border bg-card p-4" key={n}>
            <Skeleton className="h-40 w-full" />
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ))}
      </div>
    );
  return (
    <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground" role="status">
      <Spinner /> {label}
    </div>
  );
}
