import { createContext, useContext, useState } from "react";
import {
  useAuthClient,
  useSession,
  useUser,
} from "@openrails/auth-ui/react";

export type SignInMode = "login" | "register";
export const SignInContext = createContext<((mode: SignInMode) => void) | null>(
  null,
);

// The app's view of AuthKit: profile, sign-in dialog opener and sign-out.
export function useAuth() {
  const client = useAuthClient();
  const session = useSession();
  const { user: loaded, loading } = useUser();
  // Keep the profile while the same user's session rotates and /me reloads.
  const [kept, setKept] = useState(loaded);
  if (loaded && loaded !== kept) setKept(loaded);
  const user =
    loaded ??
    (session.status === "authenticated" && kept?.id === session.userId
      ? kept
      : null);
  const open = useContext(SignInContext);
  if (!open) throw new Error("Sign-in host is missing");
  return {
    user: user ?? undefined,
    loading: session.status === "loading" || (loading && !user),
    openLogin: () => open("login"),
    openRegister: () => open("register"),
    logout: () => client.signOut(),
  };
}
