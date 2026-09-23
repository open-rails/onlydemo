import { createContext, useContext } from "react";
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
  const { user, loading } = useUser();
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
