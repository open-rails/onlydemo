import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { Toaster } from "@/components/ui/sonner";
import { AuthHost } from "./auth";
import { ContentKitHost } from "./contentkit";
import { AppLayout, NotFoundPage, RequireAccount } from "./App";
import { HomePage, ChannelsPage } from "./pages/explore";
import { ChannelPage, NewChannelPage } from "./pages/channel";
import { EditPostPage, PostPage } from "./pages/post";
import { NewPostPage } from "./pages/compose";
import { AccountPage } from "./pages/account";
import { SearchPage } from "./pages/search";
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
      { path: "/p/:code/:slug?", element: <PostPage /> },
      { path: "/:language/p/:code/:slug?", element: <PostPage /> },
      { path: "/search", element: <SearchPage /> },
      { path: "/post/:id", element: <PostPage /> },
      {
        path: "/post/:id/edit",
        element: (
          <RequireAccount reason="Sign in to edit your post.">
            <EditPostPage />
          </RequireAccount>
        ),
      },
      {
        path: "/post/new",
        element: (
          <RequireAccount reason="Sign in to publish a post to your channel.">
            <NewPostPage />
          </RequireAccount>
        ),
      },
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
      <ContentKitHost navigate={(to, options) => void router.navigate(to, options)}>
        <RouterProvider router={router} />
      </ContentKitHost>
      <Toaster position="bottom-right" richColors closeButton />
    </QueryClientProvider>
  </StrictMode>,
);
