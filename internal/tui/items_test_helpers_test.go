package tui

// Small Item-inspection helpers used by migrated tests post QUM-693 (the
// ViewportModel back-compat facade is gone — tests inspect ChatList Items
// directly). These read accessors only — no mutation.

// itemContent returns a best-effort "what content does this Item carry"
// string for textual assertions. Used by negative checks ("none of the
// items should contain X") that previously walked MessageEntry.Content.
func itemContent(it Item) string {
	switch v := it.(type) {
	case *UserItem:
		return v.Text()
	case *AssistantTextItem:
		return v.Text()
	case *ToolCallItem:
		// Compose name + input + result so substring checks still hit.
		return v.Name() + " " + v.Input() + " " + v.Result()
	case *SystemNotificationItem:
		return v.Content()
	case *AutoTriggerItem:
		return v.RawMarkdown()
	default:
		return ""
	}
}

// toolID returns the protocol tool_use_id for a ToolCallItem, or "" for
// any other item type. Replaces the legacy MessageEntry.ToolID field
// inspection used by dedupe assertions.
func itemToolID(it Item) string {
	if t, ok := it.(*ToolCallItem); ok {
		return t.ToolID()
	}
	return ""
}
