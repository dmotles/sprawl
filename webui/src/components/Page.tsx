import type { ReactNode } from "react";

export function Page({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  return (
    <>
      <header className="page__header">
        <h1 className="page__title">{title}</h1>
        <p className="page__subtitle">{subtitle}</p>
      </header>
      {children}
    </>
  );
}

export function Notice({ children }: { children: ReactNode }) {
  return (
    <p className="notice">
      <span className="notice__glyph" aria-hidden="true">
        !
      </span>
      <span>{children}</span>
    </p>
  );
}
