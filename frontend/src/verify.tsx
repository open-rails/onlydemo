import { useState } from "react";
import { VerifyContactForm } from "@openrails/auth-ui";
import { useUser } from "@openrails/auth-ui/react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";

// Refused sign-in methods are proven and retried by auth-ui's
// ContactProofDialog (auth.tsx); this banner proves the address up front.
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
          <>
            <p className="text-xs text-muted-foreground">
              Sandbox: OnlyDemo sends no email; the code is in the server log.
            </p>
            <VerifyContactForm
              identifier={user.email}
              onVerified={() => setOpen(false)}
              onCancel={() => setOpen(false)}
            />
          </>
        ) : (
          <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
            Verify email
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}
