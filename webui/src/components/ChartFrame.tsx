import type { ReactNode } from "react";

/**
 * Chart frame: title, an optional measurement caveat, and a plot area with a
 * recessive grid and a real baseline/axis (per /dataviz § marks-and-anatomy).
 * Slice B ships the frame only; slice C renders marks into `children`, and the
 * hover layer /dataviz requires by default belongs with those marks.
 */
export function ChartFrame({
  title,
  caveat,
  children,
}: {
  title: string;
  caveat?: string;
  children: ReactNode;
}) {
  return (
    <figure className="chart" style={{ margin: 0 }}>
      <figcaption>
        <h2 className="chart__title">{title}</h2>
        {caveat ? <p className="chart__caveat">{caveat}</p> : null}
      </figcaption>
      <div className="chart__plot">{children}</div>
    </figure>
  );
}
