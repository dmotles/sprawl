import { formatTime, formatUsd } from "../format";

/**
 * A single-series bar chart, per /dataviz: one measure, one axis, no legend
 * (the frame's title names the series), a recessive baseline, and every bar
 * carrying its own accessible name so the series is readable without hover.
 *
 * Bars are laid out with CSS rather than SVG — one measure over N buckets needs
 * no scales beyond a percentage of the maximum.
 */
export function BarSeries({ points }: { points: { bucket: string; cost_usd: number }[] }) {
  // reduce, not Math.max(...spread): a spread puts the whole series on the
  // stack, and nothing bounds how many buckets the API returns.
  const peak = points.reduce((max, point) => Math.max(max, point.cost_usd), 0);
  return (
    /* The bars are a list so a screen reader announces how many readings the
       series holds, rather than N anonymous images. */
    <div className="bars" role="list" aria-label="Spend per bucket">
      {points.map((point) => (
        <div className="bars__slot" role="listitem" key={point.bucket}>
          <div
            className="bars__bar"
            role="img"
            aria-label={`${formatTime(point.bucket)}: ${formatUsd(point.cost_usd)}`}
            // A zero-spend bucket still gets a visible sliver, so a gap in the
            // series is distinguishable from a bucket that recorded nothing.
            style={{
              height: peak > 0 ? `max(2px, ${(point.cost_usd / peak) * 100}%)` : "2px",
            }}
          />
        </div>
      ))}
    </div>
  );
}
