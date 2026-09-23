import { createContext, useContext } from "react";
import type { User } from "./api";
interface AuthState {
  user?: User;
  loading: boolean;
  openLogin: () => void;
  openRegister: () => void;
  logout: () => Promise<void>;
}
export const AuthContext = createContext<AuthState | null>(null);
export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("Auth provider is missing");
  return value;
}
