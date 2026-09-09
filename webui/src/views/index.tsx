import type { ReactElement } from "react";
import { FleetView } from "./FleetView";
import { GoalsView } from "./GoalsView";
import { InboxView } from "./InboxView";
import { LedgerView } from "./LedgerView";
import { UsageView } from "./UsageView";
import { WorkflowsView } from "./WorkflowsView";

export type ViewSpec = {
  path: string;
  label: string;
  /** Decorative nav glyph — aria-hidden; the label carries the meaning. */
  glyph: string;
  element: ReactElement;
};

/**
 * The six v1 views. Single source of truth for the nav, the routes, and the
 * phone topbar title, so the three can never disagree.
 */
export const VIEWS: ViewSpec[] = [
  { path: "/goals", label: "Goals", glyph: "◎", element: <GoalsView /> },
  { path: "/workflows", label: "Workflows", glyph: "⇄", element: <WorkflowsView /> },
  { path: "/fleet", label: "Fleet", glyph: "◇", element: <FleetView /> },
  { path: "/ledger", label: "Event ledger", glyph: "≡", element: <LedgerView /> },
  { path: "/usage", label: "Cost & usage", glyph: "▤", element: <UsageView /> },
  { path: "/inbox", label: "Inbox", glyph: "✉", element: <InboxView /> },
];
