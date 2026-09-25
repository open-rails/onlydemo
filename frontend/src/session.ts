import { createContext, useContext, useMemo } from "react";
import type { UserProfile } from "@openrails/auth-ui/client";
import { useAuth as useAuthState } from "@openrails/auth-ui/react";

export type SignInMode = "login" | "register";
export const SignInContext = createContext<((mode: SignInMode) => void) | null>(
  null,
);

// The app's view of AuthKit: profile, sign-in dialog opener and sign-out.
// On reload auth-ui's signed-in hint stands in for the profile until /me
// loads, so the signed-in shell renders at once.
export function useAuth() {
  const auth = useAuthState();
  const open = useContext(SignInContext);
  const hintID = auth.hint?.userId;
  const hintName = auth.hint?.username;
  const standIn = useMemo(
    () => (hintID && hintID === auth.userId ? ({ id: hintID, username: hintName ?? "" } as UserProfile) : null),
    [hintID, hintName, auth.userId],
  );
  if (!open) throw new Error("Sign-in host is missing");
  const user = auth.user ?? standIn ?? undefined;
  return {
    user,
    loading: auth.status === "loading" || (auth.signedIn && !user),
    openLogin: () => open("login"),
    openRegister: () => open("register"),
    logout: () => auth.signOut(),
  };
}
