import { useEffect, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router-dom";
import {
  AuthUiProvider,
  LoginForm,
  SignInDialog,
  StepUpProvider,
} from "@openrails/auth-ui";
import {
  AuthProvider,
  useLogin,
  useSession,
} from "@openrails/auth-ui/react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { auth } from "./api";
import { Loading } from "./components/states";
import { SignInContext, type SignInMode } from "./session";

// AuthKit here soft-deletes: accounts are restorable for 30 days.
const messages = {
  account: {
    delete: {
      description:
        "Deleting your account signs you out everywhere. You can restore it within 30 days by signing in again.",
      warningAccess:
        "First transfer or delete any channel you are the last owner of. Purchases and payment history are kept.",
      warningPermanent:
        "After 30 days the account is removed for good. Restoring it does not reopen a deleted channel.",
    },
  },
};

export function AuthHost({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  return (
    <AuthProvider
      client={auth}
      onSessionChange={() => void queryClient.resetQueries()}
    >
      <AuthUiProvider appearance={{ theme: "inherit" }} messages={messages}>
        <StepUpProvider>
          <SignInHost>{children}</SignInHost>
        </StepUpProvider>
      </AuthUiProvider>
    </AuthProvider>
  );
}

function SignInHost({ children }: { children: ReactNode }) {
  const session = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const [mode, setMode] = useState<SignInMode | null>(null);
  return (
    <SignInContext.Provider value={setMode}>
      {session.status === "loading" ? (
        <Loading label="Loading your account…" />
      ) : (
        children
      )}
      <SignInDialog
        open={!!mode}
        onOpenChange={(open) => {
          if (!open) setMode(null);
        }}
        initialTab={mode ?? "login"}
        returnTo={location.pathname + location.search}
        navigate={(to) => navigate(to)}
        onSignedIn={() => setMode(null)}
      />
      <SessionContinuation />
    </SignInContext.Provider>
  );
}

// A refresh can end the session with a continuation (e.g. 2FA was just
// enabled); finish it in place instead of silently signing out.
function SessionContinuation() {
  const session = useSession();
  const continuation =
    session.status === "anonymous" ? session.continuation : null;
  const [dismissed, setDismissed] = useState<object | null>(null);
  const login = useLogin();
  const { resume } = login;
  useEffect(() => {
    if (continuation) resume(continuation);
  }, [continuation, resume]);
  const open = !!continuation && dismissed !== continuation;
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setDismissed(continuation);
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Confirm it’s you</DialogTitle>
          <DialogDescription>
            Your security settings changed. Finish signing in to continue.
          </DialogDescription>
        </DialogHeader>
        <LoginForm controller={login} />
      </DialogContent>
    </Dialog>
  );
}
