import { useNavigate } from "react-router-dom";
import { AuthCallback, ResetPasswordForm, VerifyLink } from "@openrails/auth-ui";
import { readLinkFragment } from "@openrails/auth-ui/client";
import { useAuth } from "../session";

// AuthKit's password reset link lands here (Frontend.PasswordResetPath).
export function ResetPasswordPage() {
  const auth = useAuth();
  const token = readLinkFragment(location.hash)?.token;
  return (
    <div className="page-narrow">
      <ResetPasswordForm
        token={token}
        onDone={auth.openLogin}
        onRequestNewLink={auth.openLogin}
      />
    </div>
  );
}

// OIDC sign-in returns here (Frontend.OIDCReturnPath).
export function AuthCallbackPage() {
  const navigate = useNavigate();
  return (
    <div className="page-narrow">
      <AuthCallback navigate={(to) => navigate(to, { replace: true })} />
    </div>
  );
}

// AuthKit's email verification link lands here (Frontend.VerifyPath).
export function VerifyLinkPage() {
  const navigate = useNavigate();
  return (
    <div className="page-narrow">
      <VerifyLink
        token={readLinkFragment(location.hash)?.token}
        navigate={(to) => navigate(to, { replace: true })}
      />
    </div>
  );
}
