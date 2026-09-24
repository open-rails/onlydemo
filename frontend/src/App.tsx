import { useState, type ReactNode } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./session";
import { HugeiconsIcon, type IconSvgElement } from "@hugeicons/react";
import {
  Add01Icon,
  ArrowRight02Icon,
  Cancel01Icon,
  DiscoverCircleIcon,
  Home01Icon,
  LinkSquare02Icon,
  Logout03Icon,
  Moon02Icon,
  ShoppingBag01Icon,
  SquareLock02Icon,
  Wallet01Icon,
  Sun03Icon,
  UserGroupIcon,
  UserIcon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { EmptyState, Loading } from "./components/states";
import { UserAvatar, SuggestedCreators } from "./components/cards";
import { channelsPath, newChannelPath } from "./paths";

export function AppLayout() {
  const auth = useAuth();
  const [dark, setDark] = useState(() =>
    document.documentElement.classList.contains("dark"),
  );
  const [logoutError, setLogoutError] = useState("");
  const location = useLocation();
  const toggleTheme = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("openrails-theme", next ? "dark" : "light");
    } catch {
      /* Theme remains usable without storage. */
    }
  };
  const logout = async () => {
    setLogoutError("");
    try {
      await auth.logout();
    } catch {
      setLogoutError(
        "You are signed out in this tab. The server could not confirm session revocation.",
      );
    }
  };
  const tab = new URLSearchParams(location.search).get("tab") || "library";
  const active = (to: string) => {
    const [path, query] = to.split("?");
    if (path !== location.pathname) return false;
    return !query || query === `tab=${tab}`;
  };
  const nav: [string, string, IconSvgElement][] = [
    ["/", "Home", Home01Icon],
    [channelsPath, "Explore", DiscoverCircleIcon],
    ["/me?tab=billing", "Billing", Wallet01Icon],
    ["/me?tab=library", "Purchased", ShoppingBag01Icon],
    ["/me?tab=channels", "My channels", UserGroupIcon],
    ["/me?tab=settings", "Account", UserIcon],
  ];
  const themeButton = (
    <Button
      variant="ghost"
      size="icon"
      onClick={toggleTheme}
      aria-label={`Switch to ${dark ? "light" : "dark"} theme`}
    >
      <HugeiconsIcon icon={dark ? Sun03Icon : Moon02Icon} />
    </Button>
  );
  const logoutButton = (labelled: boolean) => (
    <Button
      variant="ghost"
      size={labelled ? "sm" : "icon"}
      onClick={() => {
        void logout();
      }}
      aria-label={labelled ? undefined : "Sign out"}
      title="Sign out"
    >
      <HugeiconsIcon icon={Logout03Icon} />
      {labelled && <span>Sign out</span>}
    </Button>
  );
  return (
    <>
      <a href="#main-content" className="skip-link">
        Skip to content
      </a>
      <header className="mobile-top">
        <Link to="/" className="brand" aria-label="OnlyDemo home">
          <Logo />
        </Link>
        <div className="inline-actions">
          {themeButton}
          {!auth.loading &&
            (auth.user ? (
              logoutButton(false)
            ) : (
              <Button size="sm" onClick={auth.openLogin}>
                Sign in
              </Button>
            ))}
        </div>
      </header>
      <div className="shell">
        <aside className="sidebar">
          <Link to="/" className="brand" aria-label="OnlyDemo home">
            <Logo />
          </Link>
          {auth.user && (
            <Link to="/me" className="sidebar-user">
              <UserAvatar user={auth.user} />
              <span>
                <strong>{auth.user.username}</strong>
                <small>@{auth.user.username}</small>
              </span>
            </Link>
          )}
          <nav className="side-nav" aria-label="Main navigation">
            {nav.map(([to, label, icon]) => (
              <Link
                key={to}
                to={to}
                className={active(to) ? "active" : undefined}
                aria-current={active(to) ? "page" : undefined}
              >
                <HugeiconsIcon icon={icon} size={24} />
                <span>{label}</span>
              </Link>
            ))}
          </nav>
          <Button
            size="lg"
            className="sidebar-cta"
            nativeButton={false}
            render={<Link to={newChannelPath} />}
          >
            <HugeiconsIcon icon={Add01Icon} />
            <span>New channel</span>
          </Button>
          <div className="sidebar-footer">
            {themeButton}
            {auth.loading ? (
              <Spinner aria-label="Loading account" />
            ) : auth.user ? (
              logoutButton(true)
            ) : (
              <>
                <Button size="sm" onClick={auth.openLogin}>
                  Sign in
                </Button>
                <Button variant="outline" size="sm" onClick={auth.openRegister}>
                  Sign up
                </Button>
              </>
            )}
          </div>
        </aside>
        <main
          id="main-content"
          className="content"
          key={location.pathname}
          tabIndex={-1}
        >
          {logoutError && (
            <Alert className="mb-6" role="status">
              <AlertDescription>{logoutError}</AlertDescription>
              <AlertAction>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Dismiss"
                  onClick={() => setLogoutError("")}
                >
                  <HugeiconsIcon icon={Cancel01Icon} />
                </Button>
              </AlertAction>
            </Alert>
          )}
          <Outlet />
        </main>
        <aside className="rail">
          <SuggestedCreators />
          <div className="rail-footer">
            <Badge variant="outline" className="text-warning">
              Sandbox payments only
            </Badge>
            <a href="/dev/routes">
              Developer routes
              <HugeiconsIcon icon={LinkSquare02Icon} size={11} />
            </a>
            <span>© OnlyDemo</span>
          </div>
        </aside>
      </div>
      <nav className="tabbar" aria-label="Mobile navigation">
        {nav
          .filter(([to]) => to !== "/me?tab=channels")
          .map(([to, label, icon]) => (
            <Link
              key={to}
              to={to}
              className={active(to) ? "active" : undefined}
              aria-label={label}
            >
              <HugeiconsIcon icon={icon} size={24} />
            </Link>
          ))}
      </nav>
    </>
  );
}
function Logo() {
  return (
    <>
      <span className="brand-mark">
        <HugeiconsIcon icon={SquareLock02Icon} size={17} />
      </span>
      <span>
        Only<b>Demo</b>
      </span>
    </>
  );
}
export function RequireAccount({ children }: { children: ReactNode }) {
  const auth = useAuth();
  if (auth.loading) return <Loading label="Loading your account…" />;
  if (!auth.user)
    return (
      <div className="centered-page">
        <EmptyState
          icon={SquareLock02Icon}
          title="Sign in to continue."
          action={
            <div className="inline-actions">
              <Button onClick={auth.openLogin}>Sign in</Button>
              <Button variant="outline" onClick={auth.openRegister}>
                Create account
              </Button>
            </div>
          }
        >
          Sign in to see your subscriptions, unlocked posts, and the channels
          you run.
        </EmptyState>
      </div>
    );
  return children;
}
export function NotFoundPage() {
  return (
    <div className="centered-page">
      <span className="eyebrow">404 · Not found</span>
      <h1 style={{ marginTop: 18 }}>This page isn’t here.</h1>
      <p>
        The address may have changed, or this content may no longer be
        available.
      </p>
      <div className="inline-actions">
        <Button nativeButton={false} render={<Link to="/" />}>
          Back home
          <HugeiconsIcon icon={ArrowRight02Icon} data-icon="inline-end" />
        </Button>
      </div>
    </div>
  );
}
