import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { UploadUiProvider } from "@openrails/contentkit-upload/ui";
import { de } from "@openrails/contentkit-upload/locales/de";
import { en } from "@openrails/contentkit-upload/locales/en";
import { es } from "@openrails/contentkit-upload/locales/es";
import { ja } from "@openrails/contentkit-upload/locales/ja";
import { ko } from "@openrails/contentkit-upload/locales/ko";
import { zh } from "@openrails/contentkit-upload/locales/zh";
import { AuthHost } from "./auth";
import { uploads } from "./media";
import { AppLayout, NotFoundPage, RequireAccount } from "./App";
import { HomePage, ChannelsPage } from "./pages/explore";
import { ChannelPage, NewChannelPage } from "./pages/channel";
import { PostPage } from "./pages/post";
import { AccountPage } from "./pages/account";
import { InvitePage } from "./components/channel-team";
import {
  AuthCallbackPage,
  ResetPasswordPage,
  VerifyLinkPage,
} from "./pages/auth-links";
import "./styles.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 20_000, retry: false, refetchOnWindowFocus: true },
    mutations: { retry: false },
  },
});
// ContentKit's upload UI follows the page's shadcn tokens, .dark class and <html lang>.
const uploadLocales = { de, en, es, ja, ko, zh };
const uploadMessages =
  uploadLocales[document.documentElement.lang.slice(0, 2) as keyof typeof uploadLocales] ?? en;
const router = createBrowserRouter([
  {
    element: (
      <AuthHost>
        <AppLayout />
      </AuthHost>
    ),
    errorElement: <NotFoundPage />,
    children: [
      { path: "/", element: <HomePage /> },
      { path: "/c", element: <ChannelsPage /> },
      { path: "/c/new", element: <NewChannelPage /> },
      { path: "/c/:channel", element: <ChannelPage /> },
      { path: "/c/:channel/:post", element: <PostPage /> },
      { path: "/reset", element: <ResetPasswordPage /> },
      { path: "/verify", element: <VerifyLinkPage /> },
      { path: "/login/callback", element: <AuthCallbackPage /> },
      {
        path: "/invite",
        element: (
          <RequireAccount>
            <InvitePage />
          </RequireAccount>
        ),
      },
      {
        path: "/me",
        element: (
          <RequireAccount>
            <AccountPage />
          </RequireAccount>
        ),
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <UploadUiProvider client={uploads} appearance={{ theme: "inherit" }} messages={uploadMessages}>
        <RouterProvider router={router} />
      </UploadUiProvider>
    </QueryClientProvider>
  </StrictMode>,
);
