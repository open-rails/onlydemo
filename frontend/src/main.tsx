import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { AuthHost } from "./auth";
import { AppLayout, NotFoundPage, RequireAccount } from "./App";
import { HomePage, ChannelsPage } from "./pages/explore";
import { ChannelPage, NewChannelPage } from "./pages/channel";
import { PostPage, CheckoutReturnPage } from "./pages/post";
import { AccountPage } from "./pages/account";
import { InvitePage } from "./components/channel-team";
import { AuthCallbackPage, ResetPasswordPage } from "./pages/auth-links";
import "@openrails/billing-ui/styles.css";
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
      { path: "/channels", element: <ChannelsPage /> },
      { path: "/channels/new", element: <NewChannelPage /> },
      { path: "/channels/:slug", element: <ChannelPage /> },
      { path: "/posts/:id", element: <PostPage /> },
      { path: "/reset", element: <ResetPasswordPage /> },
      { path: "/login/callback", element: <AuthCallbackPage /> },
      {
        path: "/checkout/return",
        element: (
          <RequireAccount>
            <CheckoutReturnPage />
          </RequireAccount>
        ),
      },
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
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
