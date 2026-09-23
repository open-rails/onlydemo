import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { BillingUiProvider } from "@openrails/billing-ui";
import { BillingProvider } from "@openrails/billing-ui/react";
import { sessionIdentity, useSession } from "@openrails/auth-ui/react";
import { billing } from "./api";

export function BillingHost({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  return (
    <BillingUiProvider
      appearance={{ theme: "inherit" }}
      navigate={(to) => navigate(to)}
    >
      {children}
    </BillingUiProvider>
  );
}

// Account billing state, remounted per identity so one account's billing
// never shows for the next.
export function AccountBillingScope({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const identity = sessionIdentity(useSession());
  return (
    <BillingProvider
      key={identity}
      client={billing}
      onChange={() => void queryClient.invalidateQueries()}
    >
      {children}
    </BillingProvider>
  );
}
