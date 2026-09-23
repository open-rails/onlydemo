import { useCallback, useState, type FormEvent, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { Capabilities } from "@openrails/auth-ui/client";
import {
  AuthProvider,
  useCapabilities,
  useLogin,
  useRegister,
  useSession,
} from "@openrails/auth-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import {
  Alert02Icon,
  ArrowRight02Icon,
  Tick02Icon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { auth } from "./api";
import { FormError, Loading } from "./components/states";
import { SignInContext, type SignInMode } from "./session";

export function AuthHost({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  return (
    <AuthProvider
      client={auth}
      onSessionChange={() => void queryClient.resetQueries()}
    >
      <SignInHost>{children}</SignInHost>
    </AuthProvider>
  );
}

function SignInHost({ children }: { children: ReactNode }) {
  const session = useSession();
  const [mode, setMode] = useState<SignInMode | null>(null);
  const close = useCallback(() => setMode(null), []);
  return (
    <SignInContext.Provider value={setMode}>
      {session.status === "loading" ? <Loading label="Loading your account…" /> : children}
      <Dialog
        open={!!mode}
        onOpenChange={(open) => {
          if (!open) close();
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {mode === "register" ? "Join OnlyDemo" : "Welcome back."}
            </DialogTitle>
            <DialogDescription>
              {mode === "register"
                ? "Subscribe to creators, unlock posts, and start your own channel."
                : "Sign in to your subscriptions and channels."}
            </DialogDescription>
          </DialogHeader>
          {mode === "login" && (
            <LoginFlow onDone={close} onSwitch={() => setMode("register")} />
          )}
          {mode === "register" && (
            <RegisterFlow onDone={close} onSwitch={() => setMode("login")} />
          )}
        </DialogContent>
      </Dialog>
    </SignInContext.Provider>
  );
}

// AuthKit v0.130 capabilities also carry the naming and password policy.
type Policies = Capabilities & {
  username?: { min_length: number; max_length: number; pattern: string };
  password: PasswordPolicy;
};
type PasswordPolicy = {
  login: boolean;
  min_length?: number;
  max_length?: number;
  require_uppercase?: boolean;
  require_lowercase?: boolean;
  require_digit?: boolean;
  require_symbol?: boolean;
  reject_common?: boolean;
};

function LoginFlow({
  onDone,
  onSwitch,
}: {
  onDone: () => void;
  onSwitch: () => void;
}) {
  const login = useLogin({ onSignedIn: onDone });
  const { state, busy, error } = login;
  const submit = (event: FormEvent<HTMLFormElement>, fn: (v: string) => void, name: string) => {
    event.preventDefault();
    fn(String(new FormData(event.currentTarget).get(name)));
  };
  if (state.step === "two_factor")
    return (
      <form onSubmit={(e) => submit(e, (code) => void login.verifyTwoFactor(code), "code")}>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="twofa-code">Two-factor code</FieldLabel>
            <Input
              id="twofa-code"
              name="code"
              inputMode="numeric"
              autoComplete="one-time-code"
              required
              autoFocus
            />
            <FieldDescription>
              {state.challenge.method === "totp"
                ? "Enter the code from your authenticator app."
                : `Enter the code sent to ${state.challenge.verificationId || "you"}.`}
            </FieldDescription>
          </Field>
          <FormError>{error?.message}</FormError>
          <SubmitButton busy={busy}>Verify</SubmitButton>
          <Button
            type="button"
            variant="link"
            onClick={(event) => {
              const code = event.currentTarget.form?.elements.namedItem("code") as HTMLInputElement | null;
              if (code?.value) void login.verifyTwoFactor(code.value, { backupCode: true });
            }}
          >
            Use the entered value as a backup code
          </Button>
        </FieldGroup>
      </form>
    );
  if (state.step === "backup_codes")
    return <BackupCodes codes={state.codes} onDone={login.acknowledgeBackupCodes} />;
  if (state.step === "recovery")
    return (
      <div className="flex flex-col gap-4">
        <Alert>
          <HugeiconsIcon icon={Alert02Icon} />
          <AlertDescription>
            This account is scheduled for deletion. You can explicitly restore
            it during its recovery period. Restoring your account does not
            reopen a deleted channel.
          </AlertDescription>
        </Alert>
        <FormError>{error?.message}</FormError>
        <Button disabled={busy} onClick={() => void login.confirmRecovery()}>
          {busy && <Spinner data-icon="inline-start" />}
          Restore my account
        </Button>
        <Button variant="ghost" onClick={login.reset}>
          Back to sign in
        </Button>
      </div>
    );
  if (state.step === "verification")
    return (
      <form onSubmit={(e) => submit(e, (code) => void login.confirmVerification(code), "code")}>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="verify-code">Verification code</FieldLabel>
            <Input id="verify-code" name="code" inputMode="numeric" required autoFocus />
            <FieldDescription>
              Enter the code sent to {state.identifier}.
            </FieldDescription>
          </Field>
          <FormError>{error?.message}</FormError>
          <SubmitButton busy={busy}>Verify</SubmitButton>
          <Button type="button" variant="link" onClick={() => void login.resendVerification()}>
            Resend code
          </Button>
        </FieldGroup>
      </form>
    );
  if (state.step === "enrollment")
    return (
      <Alert>
        <AlertDescription>
          This account must enroll two-factor authentication before signing in.
        </AlertDescription>
      </Alert>
    );
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        const data = new FormData(event.currentTarget);
        void login.signIn({
          identifier: String(data.get("identifier")).trim(),
          password: String(data.get("password")),
        });
      }}
    >
      <FieldGroup>
        {state.step === "credentials" && state.recovered && (
          <Alert role="status">
            <HugeiconsIcon icon={Tick02Icon} className="text-success" />
            <AlertDescription>
              Your account is restored. Sign in to continue.
            </AlertDescription>
          </Alert>
        )}
        <Field>
          <FieldLabel htmlFor="auth-identifier">Email or username</FieldLabel>
          <Input
            id="auth-identifier"
            name="identifier"
            autoComplete="username"
            required
            autoFocus
            placeholder="you@example.com"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="auth-password">Password</FieldLabel>
          <Input
            id="auth-password"
            name="password"
            type="password"
            autoComplete="current-password"
            required
            placeholder="Enter your password"
          />
        </Field>
        <FormError>{error?.message}</FormError>
        <SubmitButton busy={busy}>Sign in</SubmitButton>
        <p className="text-center text-sm text-muted-foreground">
          New here?{" "}
          <Button type="button" variant="link" className="h-auto p-0" onClick={onSwitch}>
            Create an account
          </Button>
        </p>
        <p className="fine-print">
          Recovering a deleted account? Sign in with your existing password
          and we’ll guide you through restoration.
        </p>
      </FieldGroup>
    </form>
  );
}

function RegisterFlow({
  onDone,
  onSwitch,
}: {
  onDone: () => void;
  onSwitch: () => void;
}) {
  const register = useRegister({ onSignedIn: onDone });
  const caps = useCapabilities().capabilities as Policies | null;
  const policy = caps?.password;
  const username = caps?.username;
  const { state, busy, error } = register;
  if (state.step === "continuation")
    return (
      <Alert>
        <AlertDescription>
          Your account was created. Sign in to finish setting it up.
        </AlertDescription>
      </Alert>
    );
  if (state.step === "done")
    return (
      <Alert role="status">
        <HugeiconsIcon icon={Tick02Icon} className="text-success" />
        <AlertDescription>Your account is ready. Sign in to continue.</AlertDescription>
      </Alert>
    );
  if (state.step === "verify")
    return (
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void register.verify(String(new FormData(event.currentTarget).get("code")));
        }}
      >
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="register-code">Verification code</FieldLabel>
            <Input id="register-code" name="code" inputMode="numeric" required autoFocus />
            <FieldDescription>Enter the code sent to {state.identifier}.</FieldDescription>
          </Field>
          <FormError>{error?.message}</FormError>
          <SubmitButton busy={busy}>Verify and sign in</SubmitButton>
          <Button type="button" variant="link" onClick={() => void register.resend()}>
            Resend code
          </Button>
        </FieldGroup>
      </form>
    );
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        const data = new FormData(event.currentTarget);
        void register.register({
          identifier: String(data.get("identifier")).trim(),
          username: String(data.get("username")).trim(),
          password: String(data.get("password")),
        });
      }}
    >
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="auth-identifier">Email</FieldLabel>
          <Input
            id="auth-identifier"
            name="identifier"
            type="email"
            autoComplete="email"
            required
            autoFocus
            placeholder="you@example.com"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="auth-username">Username</FieldLabel>
          <Input
            id="auth-username"
            name="username"
            pattern={username?.pattern}
            autoComplete="nickname"
            required
            minLength={username?.min_length}
            maxLength={username?.max_length}
            placeholder="yourname"
          />
          {username && (
            <FieldDescription>
              Your public name: {username.min_length}–{username.max_length}{" "}
              characters, starting with a letter.
            </FieldDescription>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor="auth-password">Password</FieldLabel>
          <Input
            id="auth-password"
            name="password"
            type="password"
            autoComplete="new-password"
            required
            minLength={policy?.min_length}
            maxLength={policy?.max_length}
            onInput={(event) => {
              const missing = policy ? missingClasses(policy, event.currentTarget.value) : [];
              event.currentTarget.setCustomValidity(
                missing.length ? `Add ${missing.join(", ")}.` : "",
              );
            }}
            placeholder="Choose a strong password"
          />
          {policy && <FieldDescription>{passwordHint(policy)}</FieldDescription>}
        </Field>
        <FormError>{error?.message}</FormError>
        <SubmitButton busy={busy}>Create account</SubmitButton>
        <p className="text-center text-sm text-muted-foreground">
          Already have an account?{" "}
          <Button type="button" variant="link" className="h-auto p-0" onClick={onSwitch}>
            Sign in
          </Button>
        </p>
      </FieldGroup>
    </form>
  );
}

export function BackupCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  return (
    <div className="flex flex-col gap-4">
      <Alert>
        <HugeiconsIcon icon={Alert02Icon} />
        <AlertDescription>
          Save these backup codes somewhere safe. Each works once, and they are
          not shown again.
        </AlertDescription>
      </Alert>
      <ul className="grid grid-cols-2 gap-2 font-mono text-sm" aria-label="Backup codes">
        {codes.map((code) => (
          <li key={code} className="rounded-md bg-muted px-3 py-2">
            {code}
          </li>
        ))}
      </ul>
      <Button onClick={onDone}>I saved my codes</Button>
    </div>
  );
}

function SubmitButton({ busy, children }: { busy: boolean; children: ReactNode }) {
  return (
    <Button type="submit" size="lg" disabled={busy}>
      {busy && <Spinner data-icon="inline-start" />}
      {children}
      <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
    </Button>
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
  return (
    [
      `At least ${policy.min_length ?? 8} characters`,
      ...(required.length ? [`including ${required.join(", ")}`] : []),
      ...(policy.reject_common ? ["not a common password"] : []),
    ].join("; ") + "."
  );
}
