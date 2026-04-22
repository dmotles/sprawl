package cmd

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/tui"
)

func TestBuildTUIRootNotifier_DispatchesForRoot(t *testing.T) {
	var got []tea.Msg
	send := func(m tea.Msg) { got = append(got, m) }

	notify := buildTUIRootNotifier("weave", send)
	if notify == nil {
		t.Fatal("buildTUIRootNotifier returned nil for valid args")
	}

	notify("weave", "pretend-child", "hello", "msg-id")

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	msg, ok := got[0].(tui.InboxArrivalMsg)
	if !ok {
		t.Fatalf("got[0] = %T, want tui.InboxArrivalMsg", got[0])
	}
	if msg.From != "pretend-child" {
		t.Errorf("From = %q, want %q", msg.From, "pretend-child")
	}
	if msg.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "hello")
	}
}

func TestBuildTUIRootNotifier_FiltersOnRecipient(t *testing.T) {
	var got []tea.Msg
	send := func(m tea.Msg) { got = append(got, m) }

	notify := buildTUIRootNotifier("weave", send)

	// A message addressed to a child agent must not be dispatched to the TUI;
	// the tree panel already polls each child's maildir via tickAgentsCmd.
	notify("pretend-child", "tower", "hi", "msg-id")

	if len(got) != 0 {
		t.Errorf("notifier dispatched %d messages for non-root recipient, want 0", len(got))
	}
}

func TestBuildTUIRootNotifier_NilWhenSendMissing(t *testing.T) {
	if buildTUIRootNotifier("weave", nil) != nil {
		t.Error("buildTUIRootNotifier(send=nil) should return nil")
	}
}

func TestBuildTUIRootNotifier_NilWhenRootNameMissing(t *testing.T) {
	send := func(tea.Msg) {}
	if buildTUIRootNotifier("", send) != nil {
		t.Error("buildTUIRootNotifier(rootName=\"\") should return nil")
	}
}
