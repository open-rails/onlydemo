import { useState, type ReactNode } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./auth-context";
import { Button, Icon, type IconName } from "./components/ui";
import { Avatar, SuggestedCreators } from "./components/cards";

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
  const nav: [string, string, IconName][] = [
    ["/", "Home", "home"],
    ["/channels", "Explore", "compass"],
    ["/me?tab=subscriptions", "Subscriptions", "star"],
    ["/me?tab=library", "Purchased", "bag"],
    ["/me?tab=channels", "My channels", "users"],
    ["/me?tab=settings", "Account", "user"],
  ];
  const themeButton = (
    <button
      className="icon-button"
      onClick={toggleTheme}
      aria-label={`Switch to ${dark ? "light" : "dark"} theme`}
    >
      <Icon name={dark ? "sun" : "moon"} size={20} />
    </button>
  );
  const logoutButton = (
    <button
      className="icon-button"
      onClick={() => {
        void logout();
      }}
      aria-label="Sign out"
      title="Sign out"
    >
      <Icon name="logout" size={20} />
    </button>
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
              logoutButton
            ) : (
              <Button className="button-sm" onClick={auth.openLogin}>
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
              <Avatar name={auth.user.username} seed={auth.user.id} />
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
                <Icon name={icon} size={24} />
                <span>{label}</span>
              </Link>
            ))}
          </nav>
          <Link to="/channels/new" className="button button-primary sidebar-cta">
            <Icon name="plus" size={18} />
            <span>New channel</span>
          </Link>
          <div className="sidebar-footer">
            {themeButton}
            {auth.loading ? (
              <span className="spinner muted" aria-label="Loading account" />
            ) : auth.user ? (
              logoutButton
            ) : (
              <>
                <Button className="button-sm" onClick={auth.openLogin}>
                  Sign in
                </Button>
                <Button
                  variant="secondary"
                  className="button-sm"
                  onClick={auth.openRegister}
                >
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
            <div
              className="notice notice-warning"
              role="status"
              style={{ marginBottom: 24 }}
            >
              {logoutError}
              <button
                className="icon-button"
                aria-label="Dismiss"
                onClick={() => setLogoutError("")}
              >
                <Icon name="close" />
              </button>
            </div>
          )}
          <Outlet />
        </main>
        <aside className="rail">
          <SuggestedCreators />
          <div className="rail-footer">
            <span className="badge badge-warning">Stripe test environment</span>
            <a href="/dev/routes">
              Developer routes
              <Icon name="external" size={11} />
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
              <Icon name={icon} size={24} />
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
        <Icon name="lock" size={17} />
      </span>
      <span>
        Only<b>Demo</b>
      </span>
    </>
  );
}
export function RequireAccount({ children }: { children: ReactNode }) {
  const auth = useAuth();
  if (auth.loading)
    return (
      <div className="loading">
        <span className="spinner" />
        Loading your account…
      </div>
    );
  if (!auth.user)
    return (
      <div className="centered-page">
        <div className="empty-icon">
          <Icon name="lock" size={28} />
        </div>
        <span className="eyebrow">OnlyDemo</span>
        <h1 style={{ marginTop: 16 }}>Sign in to continue.</h1>
        <p>
          Sign in to see your subscriptions, unlocked posts, and the channels
          you run.
        </p>
        <div className="inline-actions">
          <Button onClick={auth.openLogin}>Sign in</Button>
          <Button variant="secondary" onClick={auth.openRegister}>
            Create account
          </Button>
        </div>
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
        <Link className="button button-primary" to="/">
          Back home
          <Icon name="arrow" size={16} />
        </Link>
      </div>
    </div>
  );
}
