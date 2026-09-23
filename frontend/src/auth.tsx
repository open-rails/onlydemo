import {
  useState,
  useSyncExternalStore,
  type FormEvent,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  authAPI,
  getSession,
  setSession,
  subscribeSession,
} from "./api";
import { Button, Field, Icon, Modal } from "./components/ui";
import { AuthContext } from "./auth-context";

export function AuthProvider({ children }: { children: ReactNode }) {
  const session = useSyncExternalStore(subscribeSession, getSession);
  const client = useQueryClient();
  const profile = useQuery({
    queryKey: ["auth", "me", session?.access_token],
    queryFn: authAPI.me,
    enabled: !!session,
    retry: false,
    staleTime: 60_000,
  });
  const [dialog, setDialog] = useState<"login" | "register" | null>(null);
  const logout = async () => {
    try {
      await authAPI.logout();
    } finally {
      setSession(null);
      client.clear();
    }
  };
  return (
    <AuthContext.Provider
      value={{
        user: profile.data,
        loading: !!session && profile.isPending,
        openLogin: () => setDialog("login"),
        openRegister: () => setDialog("register"),
        logout,
      }}
    >
      {children}
      <Modal
        open={!!dialog}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
        title={
          dialog === "register" ? "Find your next great read." : "Welcome back."
        }
        description={
          dialog === "register"
            ? "Create an account to follow creators, buy stories, and start your own channel."
            : "Sign in to your reading library and channels."
        }
      >
        {dialog && (
          <AuthForm
            key={dialog}
            mode={dialog}
            onSwitch={() =>
              setDialog(dialog === "login" ? "register" : "login")
            }
            onSuccess={() => {
              setDialog(null);
              void client.invalidateQueries();
            }}
          />
        )}
      </Modal>
    </AuthContext.Provider>
  );
}
function AuthForm({
  mode,
  onSwitch,
  onSuccess,
}: {
  mode: "login" | "register";
  onSwitch: () => void;
  onSuccess: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [recovery, setRecovery] = useState<string | null>(null);
  const [restored, setRestored] = useState(false);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setRestored(false);
    setBusy(true);
    const data = new FormData(event.currentTarget);
    try {
      const identifier = String(data.get("identifier")).trim(),
        password = String(data.get("password"));
      const tokens =
        mode === "register"
          ? (
              await authAPI.register(
                identifier,
                String(data.get("username")).trim(),
                password,
              )
            ).token_set
          : await authAPI.login(identifier, password);
      if (!tokens?.access_token)
        throw new Error(
          "Sign-in could not be completed. Please try signing in.",
        );
      setSession(tokens);
      onSuccess();
    } catch (cause) {
      const metadata =
        cause instanceof APIError
          ? (cause.metadata?.recovery as { token?: string } | undefined)
          : undefined;
      if (metadata?.token) setRecovery(metadata.token);
      else
        setError(
          cause instanceof Error
            ? cause.message
            : "Could not connect. Please try again.",
        );
    } finally {
      setBusy(false);
    }
  };
  const restore = async () => {
    if (!recovery) return;
    setBusy(true);
    setError("");
    try {
      await authAPI.recover(recovery);
      setRecovery(null);
      setRestored(true);
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Recovery could not be completed.",
      );
    } finally {
      setBusy(false);
    }
  };
  if (recovery)
    return (
      <div className="stack">
        <div className="notice">
          <Icon name="warning" />
          <p>
            This account is scheduled for deletion. You can explicitly restore
            it during its recovery period. Restoring your account does not
            reopen a deleted channel.
          </p>
        </div>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <Button
          busy={busy}
          onClick={() => {
            void restore();
          }}
        >
          Restore my account
        </Button>
        <Button variant="ghost" onClick={() => setRecovery(null)}>
          Back to sign in
        </Button>
      </div>
    );
  return (
    <form
      className="stack"
      onSubmit={(event) => {
        void submit(event);
      }}
    >
      {restored && (
        <div className="notice notice-success" role="status">
          <Icon name="check" />
          <p>Your account is restored. Sign in to continue.</p>
        </div>
      )}
      <Field label="Email or username">
        <input
          name="identifier"
          autoComplete="username"
          required
          autoFocus
          placeholder="you@example.com"
        />
      </Field>
      {mode === "register" && (
        <Field
          label="Username"
          hint="Your public name. Letters, numbers, and dashes work well."
        >
          <input
            name="username"
            autoComplete="nickname"
            required
            minLength={3}
            maxLength={64}
            placeholder="yourname"
          />
        </Field>
      )}
      <Field label="Password">
        <input
          name="password"
          type="password"
          autoComplete={
            mode === "register" ? "new-password" : "current-password"
          }
          required
          minLength={mode === "register" ? 10 : 1}
          placeholder={
            mode === "register"
              ? "Choose a strong password"
              : "Enter your password"
          }
        />
      </Field>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <Button type="submit" busy={busy}>
        {mode === "register" ? "Create account" : "Sign in"}
        <Icon name="arrow" size={17} />
      </Button>
      <p className="auth-switch">
        {mode === "register" ? "Already have an account?" : "New here?"}{" "}
        <button type="button" className="text-button" onClick={onSwitch}>
          {mode === "register" ? "Sign in" : "Create an account"}
        </button>
      </p>
      {mode === "login" && (
        <p className="fine-print">
          Recovering a deleted account? Sign in with your existing password and
          we’ll guide you through restoration.
        </p>
      )}
    </form>
  );
}
