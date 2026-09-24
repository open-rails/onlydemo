import { createContext, useContext, useState } from "react";
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
  const [hinted, setHinted] = useState<UserProfile | null>(null);
  if (auth.hint && hinted?.id !== auth.hint.userId)
    setHinted({
      id: auth.hint.userId,
      username: auth.hint.username ?? "",
    } as UserProfile);
  if (!open) throw new Error("Sign-in host is missing");
  const standIn = hinted && hinted.id === auth.userId ? hinted : null;
  const user = auth.user ?? standIn ?? undefined;
  return {
    user,
    loading: auth.status === "loading" || (auth.signedIn && !user),
    openLogin: () => open("login"),
    openRegister: () => open("register"),
    logout: () => auth.signOut(),
  };
}
