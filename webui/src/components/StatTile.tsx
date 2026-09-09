/**
 * Stat tile. Per /dataviz: a single headline number is a tile, not a chart, and
 * the value wears text ink rather than a series colour.
 *
 * `value` is nullable on purpose — until slice C wires the API there is no
 * number, and an em dash is an honest "not measured yet" where a 0 would be a
 * lie. `note` is where a measurement caveat goes (e.g. cost totals are lower
 * bounds because the events that carry them are spillable).
 */
export function StatTile({
  label,
  value,
  note,
}: {
  label: string;
  value?: string | null;
  note?: string;
}) {
  return (
    <div className="tile">
      <div className="tile__label">{label}</div>
      <div className="tile__value">{value ?? "—"}</div>
      {note ? <div className="tile__note">{note}</div> : null}
    </div>
  );
}
