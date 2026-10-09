import { useQuery } from "@tanstack/react-query";
import { request } from "./api";
import type { AppConfig } from "./models";

/** The app's public configuration (PSPs, the user avatar preset). */
export const useAppConfig = () =>
  useQuery({ queryKey: ["config"], queryFn: () => request<AppConfig>("/api/v1/config", {}, false), staleTime: Infinity });
