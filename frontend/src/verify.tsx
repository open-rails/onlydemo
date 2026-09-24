import { useEffect, useRef, useState } from "react";
import { useContactVerification, useUser } from "@openrails/auth-ui/react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { onUnprovenContact, type UnprovenContact } from "./api";
import { FormError } from "./components/states";

// Sends a code to identifier and confirms it. OnlyDemo has no mail sender:
// its sandbox outbox writes codes to the server log.
function VerifyContact({
  identifier,
  onVerified,
  onCancel,
}: {
  identifier: string;
  onVerified: () => void;
  onCancel?: () => void;
}) {
  const verification = useContactVerification();
  const { state, busy, error } = verification;
  const [code, setCode] = useState("");
  const verified = useRef(onVerified);
  useEffect(() => {
    verified.current = onVerified;
  });
  useEffect(() => {
    if (state.step === "done") verified.current();
  }, [state.step]);
  const cancel = onCancel && (
    <Button type="button" variant="ghost" disabled={busy} onClick={onCancel}>
      Cancel
    </Button>
  );
  if (state.step !== "code_sent")
    return (
      <div className="flex flex-col gap-3">
        <FormError>{error?.message}</FormError>
        <div className="flex gap-2">
          <Button
            disabled={busy}
            onClick={() => void verification.request(identifier)}
          >
            {busy && <Spinner data-icon="inline-start" />}
            Send code
          </Button>
          {cancel}
        </div>
      </div>
    );
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault();
        void verification.confirm(code);
      }}
    >
      <Field>
        <FieldLabel htmlFor="verify-code">Code sent to {identifier}</FieldLabel>
        <Input
          id="verify-code"
          value={code}
          onChange={(event) => setCode(event.target.value)}
          autoComplete="one-time-code"
          inputMode="numeric"
          autoFocus
        />
      </Field>
      <p className="text-xs text-muted-foreground">
        Sandbox: OnlyDemo sends no email; the code is in the server log.
      </p>
      <FormError>{error?.message}</FormError>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy || !code.trim()}>
          {busy && <Spinner data-icon="inline-start" />}
          Verify
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={() => void verification.resend()}
        >
          Send a new code
        </Button>
        {cancel}
      </div>
    </form>
  );
}

type Pending = { contact: UnprovenContact; resolve: (ok: boolean) => void };

// Answers AuthKit's contact_unproven refusals: the refused request is retried
// once the user proves the address.
export function ContactVerificationHost() {
  const [pending, setPending] = useState<Pending | null>(null);
  useEffect(
    () =>
      onUnprovenContact(
        (contact) =>
          new Promise<boolean>((resolve) =>
            setPending((current) => {
              current?.resolve(false);
              return { contact, resolve };
            }),
          ),
      ),
    [],
  );
  const finish = (ok: boolean) => {
    pending?.resolve(ok);
    setPending(null);
  };
  const email = pending?.contact.channel !== "phone";
  return (
    <Dialog
      open={!!pending}
      onOpenChange={(open) => {
        if (!open) finish(false);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Verify your {email ? "email" : "phone number"} first
          </DialogTitle>
          <DialogDescription>
            New sign-in methods need a verified{" "}
            {email ? "email address" : "phone number"}. We’ll send a code to{" "}
            {pending?.contact.identifier}, then finish what you started.
          </DialogDescription>
        </DialogHeader>
        {pending && (
          <VerifyContact
            key={pending.contact.identifier}
            identifier={pending.contact.identifier}
            onVerified={() => finish(true)}
            onCancel={() => finish(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

export function VerifyEmailBanner() {
  const { user } = useUser();
  const [open, setOpen] = useState(false);
  if (!user?.email || user.email_verified) return null;
  return (
    <Alert className="mb-6">
      <AlertTitle>Verify your email</AlertTitle>
      <AlertDescription>
        <p>
          Confirm {user.email} to add two-factor authentication or other
          sign-in methods, and to recover your account.
        </p>
        {open ? (
          <VerifyContact
            identifier={user.email}
            onVerified={() => setOpen(false)}
            onCancel={() => setOpen(false)}
          />
        ) : (
          <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
            Verify email
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}
