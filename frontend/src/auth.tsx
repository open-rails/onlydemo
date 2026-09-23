import {
  useState,
  useSyncExternalStore,
  type FormEvent,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  type PasswordPolicy,
  authAPI,
  getSession,
  getSessionGeneration,
  setSession,
  subscribeSession,
} from "./api";
import { Button, Field, Icon, Modal } from "./components/ui";
import { AuthContext } from "./auth-context";

export function AuthProvider({ children }: { children: ReactNode }) {
  const session = useSyncExternalStore(subscribeSession, getSession);
  const client = useQueryClient();
  const profile = useQuery({
    queryKey: ["auth", "me", getSessionGeneration()],
    queryFn: authAPI.me,
    enabled: !!session,
    retry: false,
    staleTime: 60_000,
  });
  const [dialog, setDialog] = useState<"login" | "register" | null>(null);
  const logout = async () => {
    const revocation = authAPI.logout();
    setSession(null);
    client.clear();
    await revocation;
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
          dialog === "register" ? "Join OnlyDemo" : "Welcome back."
        }
        description={
          dialog === "register"
            ? "Subscribe to creators, unlock posts, and start your own channel."
            : "Sign in to your subscriptions and channels."
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
  const capabilities = useQuery({
    queryKey: ["auth", "capabilities"],
    queryFn: authAPI.capabilities,
    staleTime: Infinity,
  }).data;
  const policy = capabilities?.password,
    username = capabilities?.username;
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
          hint={
            username &&
            `Your public name: ${username.min_length}–${username.max_length} characters, starting with a letter.`
          }
        >
          <input
            name="username"
            pattern={username?.pattern}
            autoComplete="nickname"
            required
            minLength={username?.min_length}
            maxLength={username?.max_length}
            placeholder="yourname"
          />
        </Field>
      )}
      <Field
        label="Password"
        hint={
          mode === "register" && policy ? passwordHint(policy) : undefined
        }
      >
        <input
          name="password"
          type="password"
          autoComplete={
            mode === "register" ? "new-password" : "current-password"
          }
          required
          minLength={mode === "register" ? policy?.min_length : 1}
          maxLength={mode === "register" ? policy?.max_length : undefined}
          onInput={(event) => {
            const missing =
              mode === "register" && policy
                ? missingClasses(policy, event.currentTarget.value)
                : [];
            event.currentTarget.setCustomValidity(
              missing.length ? `Add ${missing.join(", ")}.` : "",
            );
          }}
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

const passwordClasses = [
  ["require_uppercase", "an uppercase letter", /\p{Lu}/u],
  ["require_lowercase", "a lowercase letter", /\p{Ll}/u],
  ["require_digit", "a digit", /\p{Nd}/u],
  ["require_symbol", "a symbol", /[^\p{L}\p{Nd}]/u],
] as const;
function missingClasses(policy: PasswordPolicy, value: string) {
  return passwordClasses
    .filter(([key, , test]) => policy[key] && !test.test(value))
    .map(([, label]) => label);
}
function passwordHint(policy: PasswordPolicy) {
  const required = passwordClasses
    .filter(([key]) => policy[key])
    .map(([, label]) => label);
  return [
    `At least ${policy.min_length} characters`,
    ...(required.length ? [`including ${required.join(", ")}`] : []),
    ...(policy.reject_common ? ["not a common password"] : []),
  ].join("; ") + ".";
}
