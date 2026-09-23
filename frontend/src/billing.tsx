import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { BillingUiProvider } from "@openrails/billing-ui";
import { createBillingClient } from "@openrails/billing-ui/client";
import { BillingProvider } from "@openrails/billing-ui/react";
import { sessionIdentity, useSession } from "@openrails/auth-ui/react";
import { auth } from "./api";

const billing = createBillingClient({
  baseUrl: "/billing/v1",
  fetch: auth.authFetch,
});

export function BillingHost({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  // Remount per identity so one account's billing never shows for the next.
  const identity = sessionIdentity(useSession());
  return (
    <BillingUiProvider
      appearance={{ theme: "inherit" }}
      navigate={(to) => navigate(to)}
    >
      <BillingProvider
        key={identity}
        client={billing}
        onChange={() => void queryClient.invalidateQueries()}
      >
        {children}
      </BillingProvider>
    </BillingUiProvider>
  );
}
