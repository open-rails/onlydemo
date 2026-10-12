import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { BillingProvider } from "@openrails/billing-ui/react";
import { sessionIdentity, useSession } from "@openrails/auth-ui/react";
import { billing } from "./api";
import { PayHost } from "./components/pay";

export function BillingHost({ children }: { children: ReactNode }) {
  return <PayHost>{children}</PayHost>;
}

// Account billing state, remounted per identity so one account's billing
// never shows for the next.
export function AccountBillingScope({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const identity = sessionIdentity(useSession());
  const navigate = useNavigate();
  return (
    <BillingProvider
      key={identity}
      client={billing}
      appearance={{ theme: "inherit" }}
      navigate={(to) => navigate(to)}
      onChange={() => void queryClient.invalidateQueries()}
    >
      {children}
    </BillingProvider>
  );
}
