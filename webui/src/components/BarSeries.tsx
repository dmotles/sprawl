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
  const peak = Math.max(...points.map((point) => point.cost_usd), 0);
  return (
    <div className="bars">
      {points.map((point) => (
        <div className="bars__slot" key={point.bucket}>
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
