import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { VIEWS } from "./views";

/**
 * App shell: a persistent side nav on desktop that becomes an off-canvas drawer
 * behind a topbar button at phone widths (the breakpoint lives in app.css; this
 * component only owns the open/closed state).
 */
export function App() {
  const location = useLocation();
  const [navOpen, setNavOpen] = useState(false);

  // Navigating closes the drawer. On desktop the drawer state is inert — the
  // nav is always visible — so this is a no-op there rather than a special case.
  useEffect(() => {
    setNavOpen(false);
  }, [location.pathname]);

  const current = VIEWS.find((v) => v.path === location.pathname);

  return (
    <div className="shell" data-nav-open={navOpen}>
      <nav className="nav" aria-label="Views">
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
            aria-expanded={navOpen}
            onClick={() => setNavOpen(true)}
          >
            <span aria-hidden="true">≡</span>
          </button>
          <span className="topbar__title">{current?.label ?? "sprawl"}</span>
        </div>
        <div className="shell__content">
          <Routes>
            <Route path="/" element={<Navigate to={VIEWS[0].path} replace />} />
            {VIEWS.map((view) => (
              <Route key={view.path} path={view.path} element={view.element} />
            ))}
            <Route path="*" element={<Navigate to={VIEWS[0].path} replace />} />
          </Routes>
        </div>
      </main>
    </div>
  );
}
