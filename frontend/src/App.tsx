import { useState, type ReactNode } from 'react'
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from './auth'
import { Button, Icon } from './components/ui'

export function AppLayout() {
  const auth = useAuth()
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const [logoutError, setLogoutError] = useState('')
  const location = useLocation()
  const toggleTheme = () => {
    const next = !dark; setDark(next); document.documentElement.classList.toggle('dark', next)
    try { localStorage.setItem('openrails-theme', next ? 'dark' : 'light') } catch { /* Theme remains usable without storage. */ }
  }
  const logout = async () => { setLogoutError(''); try { await auth.logout() } catch { setLogoutError('You are signed out in this tab. The server could not confirm session revocation.') } }
  return <>
    <a href="#main-content" className="skip-link">Skip to content</a>
    <header className="app-header"><div className="header-inner">
      <Link to="/" className="brand" aria-label="OpenRails home"><span className="brand-mark"><Icon name="book" size={20} /></span>OpenRails<span className="brand-divider" /></Link>
      <nav className="main-nav" aria-label="Main navigation"><NavLink to="/" end>Explore</NavLink><NavLink to="/channels">Channels</NavLink><NavLink to="/me">My library</NavLink></nav>
      <div className="header-actions"><button className="icon-button" onClick={toggleTheme} aria-label={`Switch to ${dark ? 'light' : 'dark'} theme`}><Icon name={dark ? 'sun' : 'moon'} size={19} /></button>
        {auth.loading ? <span className="spinner muted" aria-label="Loading account" /> : auth.user ? <><Link to="/me" className="user-chip"><span className="avatar">{auth.user.username.slice(0, 1).toUpperCase()}</span><span>{auth.user.username}</span></Link><button className="icon-button" onClick={() => { void logout() }} aria-label="Sign out" title="Sign out"><Icon name="logout" size={18} /></button></> : <><Button variant="ghost" onClick={auth.openLogin}>Sign in</Button><Button onClick={auth.openRegister}>Get started</Button></>}
      </div>
    </div></header>
    <main id="main-content" className="page" key={location.pathname} tabIndex={-1}>{logoutError && <div className="notice notice-warning" role="status" style={{ marginBottom: 24 }}>{logoutError}<button className="icon-button" aria-label="Dismiss" onClick={() => setLogoutError('')}><Icon name="close" /></button></div>}<Outlet /></main>
    <footer className="app-footer"><div className="footer-inner"><span>OpenRails · Independent stories, direct support.</span><div className="footer-links"><span className="badge badge-warning">Stripe test environment</span><a href="/dev/routes">Developer routes<Icon name="external" size={11} /></a></div></div></footer>
  </>
}
export function RequireAccount({ children }: { children: ReactNode }) {
  const auth = useAuth()
  if (auth.loading) return <div className="loading"><span className="spinner" />Loading your account…</div>
  if (!auth.user) return <div className="centered-page"><div className="empty-icon"><Icon name="book" size={28} /></div><span className="eyebrow">Your reading space</span><h1 style={{ marginTop: 16 }}>All your stories, together.</h1><p>Sign in to revisit purchased posts, manage memberships, and publish to your channels.</p><div className="inline-actions"><Button onClick={auth.openLogin}>Sign in</Button><Button variant="secondary" onClick={auth.openRegister}>Create account</Button></div></div>
  return children
}
export function NotFoundPage() { return <div className="centered-page"><span className="eyebrow">404 · Not found</span><h1 style={{ marginTop: 18 }}>This page isn’t here.</h1><p>The address may have changed, or this content may no longer be available.</p><div className="inline-actions"><Link className="button button-primary" to="/">Back to explore<Icon name="arrow" size={16} /></Link></div></div> }
