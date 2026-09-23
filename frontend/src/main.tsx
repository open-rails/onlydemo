import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import { AuthProvider } from './auth'
import { AppLayout, NotFoundPage, RequireAccount } from './App'
import { HomePage, ChannelsPage } from './pages/explore'
import { ChannelPage } from './pages/channel'
import { PostPage, CheckoutReturnPage } from './pages/post'
import { AccountPage } from './pages/account'
import './styles.css'

const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: 20_000, retry: false, refetchOnWindowFocus: true }, mutations: { retry: false } } })
const router = createBrowserRouter([{ element: <AuthProvider><AppLayout /></AuthProvider>, errorElement: <NotFoundPage />, children: [
  { path: '/', element: <HomePage /> },
  { path: '/channels', element: <ChannelsPage /> },
  { path: '/channels/:id', element: <ChannelPage /> },
  { path: '/posts/:id', element: <PostPage /> },
  { path: '/checkout/return', element: <RequireAccount><CheckoutReturnPage /></RequireAccount> },
  { path: '/me', element: <RequireAccount><AccountPage /></RequireAccount> },
  { path: '*', element: <NotFoundPage /> },
] }])
createRoot(document.getElementById('root')!).render(<StrictMode><QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider></StrictMode>)
