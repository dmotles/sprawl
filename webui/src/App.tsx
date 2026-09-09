import { useEffect, useState, useSyncExternalStore } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { VIEWS } from "./views";

/** Must match the drawer breakpoint in app.css. */
const PHONE_QUERY = "(max-width: 720px)";

/**
 * Whether the nav is currently an off-canvas drawer rather than a persistent
 * sidebar. The CSS owns the layout; JS needs the same answer to keep a
 * translated-offscreen drawer out of the tab order and the a11y tree — a
 * transform hides an element visually but leaves it focusable.
 */
function usePhoneLayout() {
  return useSyncExternalStore(
    (onChange) => {
      const mq = window.matchMedia(PHONE_QUERY);
      mq.addEventListener("change", onChange);
      return () => mq.removeEventListener("change", onChange);
    },
    () => window.matchMedia(PHONE_QUERY).matches,
    () => false,
  );
}

/**
 * App shell: a persistent side nav on desktop that becomes an off-canvas drawer
 * behind a topbar button at phone widths (the breakpoint lives in app.css; this
 * component only owns the open/closed state).
 */
export function App() {
  const location = useLocation();
  const [navOpen, setNavOpen] = useState(false);
  const phone = usePhoneLayout();

  // Navigating closes the drawer. On desktop the drawer state is inert — the
  // nav is always visible — so this is a no-op there rather than a special case.
  useEffect(() => {
    setNavOpen(false);
  }, [location.pathname]);

  // Escape closes the drawer, the convention for any dismissible overlay.
  useEffect(() => {
    if (!navOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setNavOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navOpen]);

  const current = VIEWS.find((v) => v.path === location.pathname);

  return (
    <div className="shell" data-nav-open={navOpen}>
      <nav className="nav" id="app-nav" aria-label="Views" inert={phone && !navOpen}>
        <div className="nav__brand">
          <span className="nav__mark" aria-hidden="true" />
          <span className="nav__wordmark">sprawl</span>
        </div>
        {VIEWS.map((view) => (
          <NavLink key={view.path} to={view.path} className="nav__link">
            <span className="nav__glyph" aria-hidden="true">
              {view.glyph}
            </span>
            {view.label}
          </NavLink>
        ))}
        <p className="nav__footer">Read-only view of the hub event log.</p>
      </nav>

      <button
        type="button"
        className="shell__scrim"
        aria-label="Close navigation"
        onClick={() => setNavOpen(false)}
      />

      <main className="shell__main">
        <div className="topbar">
          <button
            type="button"
            className="iconbutton"
            aria-label="Open navigation"
            aria-controls="app-nav"
            aria-expanded={navOpen}
            onClick={() => setNavOpen(true)}
          >
            <span aria-hidden="true">≡</span>
          </button>
          <span className="topbar__title">{current?.label ?? "sprawl"}</span>
        </div>
        <div className="shell__content">
          {/* Inside the shell, so a crashed view leaves the nav usable, and
              keyed by path so navigating away clears the caught error rather
              than pinning the message on every later route. */}
          <ErrorBoundary key={location.pathname}>
            <Routes>
              <Route path="/" element={<Navigate to={VIEWS[0].path} replace />} />
              {VIEWS.map((view) => (
                <Route key={view.path} path={view.path} element={view.element} />
              ))}
              <Route path="*" element={<Navigate to={VIEWS[0].path} replace />} />
            </Routes>
          </ErrorBoundary>
        </div>
      </main>
    </div>
  );
}
