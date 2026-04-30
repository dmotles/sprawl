package cmd

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/spf13/cobra"
)

// messagesDeps holds the dependencies for the messages commands, enabling testability.
type messagesDeps struct {
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

var defaultMessagesDeps *messagesDeps

func init() {
	messagesCmd.AddCommand(messagesSendCmd)
	messagesCmd.AddCommand(messagesInboxCmd)
	messagesCmd.AddCommand(messagesReadCmd)
	messagesCmd.AddCommand(messagesListCmd)
	messagesCmd.AddCommand(messagesArchiveCmd)
	messagesCmd.AddCommand(messagesUnreadCmd)
	messagesCmd.AddCommand(messagesBroadcastCmd)
	messagesCmd.AddCommand(messagesSentCmd)
	rootCmd.AddCommand(messagesCmd)
}

var messagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Send and receive messages between agents",
}

var messagesSendCmd = &cobra.Command{
	Use:   "send <agent> <subject> <body>",
	Short: "Send a message to another agent",
	Args:  cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesSend(deps, args[0], args[1], args[2])
	},
}

var (
	inboxShowAll bool
	inboxNewOnly bool // kept for backward compat, now a no-op
)

var messagesInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Show messages in your inbox",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveMessagesDeps()
		return runMessagesInboxDisplay(deps, inboxShowAll)
	},
}

func init() {
	messagesInboxCmd.Flags().BoolVar(&inboxShowAll, "all", false, "Show all messages (read and unread)")
	messagesInboxCmd.Flags().BoolVar(&inboxNewOnly, "new", false, "Show only unread messages (default, kept for backward compatibility)")
}

var messagesBroadcastCmd = &cobra.Command{
	Use:   "broadcast <subject> <body>",
	Short: "Broadcast a message to all active agents",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesBroadcast(deps, args[0], args[1])
	},
}

var messagesSentCmd = &cobra.Command{
	Use:   "sent",
	Short: "Show sent messages",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveMessagesDeps()
		return runMessagesSentDisplay(deps)
	},
}

func resolveMessagesDeps() *messagesDeps {
	if defaultMessagesDeps != nil {
		return defaultMessagesDeps
	}
	return &messagesDeps{
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func formatInboxTable(w io.Writer, msgs []*messages.Message) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, msg := range msgs {
		var status string
		switch msg.Dir {
		case "new":
			status = "NEW"
		case "cur":
			status = "read"
		default:
			status = msg.Dir
		}
		ts := msg.Timestamp
		if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", displayShortID(msg), status, ts, msg.From, msg.Subject)
	}
	_ = tw.Flush()
}

func (d *messagesDeps) resolveEnv() (agentName, sprawlRoot string, err error) {
	agentName = d.getenv("SPRAWL_AGENT_IDENTITY")
	if agentName == "" {
		return "", "", fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set")
	}
	sprawlRoot = d.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return "", "", fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}
	return agentName, sprawlRoot, nil
}

func runMessagesInboxDisplay(deps *messagesDeps, showAll bool) error {
	msgs, newCount, readCount, err := runMessagesInbox(deps)
	if err != nil {
		return err
	}

	if showAll {
		fmt.Fprintf(deps.stderr, "Inbox: %d new, %d read (%d total)\n", newCount, readCount, len(msgs))
		formatInboxTable(deps.stdout, msgs)
		return nil
	}

	// Default: show only unread messages
	var unreadMsgs []*messages.Message
	for _, msg := range msgs {
		if msg.Dir == "new" {
			unreadMsgs = append(unreadMsgs, msg)
		}
	}

	if len(unreadMsgs) == 0 {
		fmt.Fprintf(deps.stderr, "No new messages.\n")
		return nil
	}

	fmt.Fprintf(deps.stderr, "Inbox: %d unread messages\n", len(unreadMsgs))
	formatInboxTable(deps.stdout, unreadMsgs)
	return nil
}

func runMessagesSend(deps *messagesDeps, to, subject, body string) error {
	deprecationWarning("messages send", "send_async")
	if err := agent.ValidateName(to); err != nil {
		return err
	}

	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return err
	}

	shortID, err := messages.Send(sprawlRoot, agentName, to, subject, body)
	if err != nil {
		return err
	}

	// QUM-323: populate the recipient's harness queue too, matching the shape
	// `sprawl report` uses (internal/agentops/report.go). This is what lets
	// the weave root-loop drain and inject the body into Claude's next prompt
	// without depending on the legacy tmux send-keys path. Enqueue failures
	// are non-fatal — the maildir delivery already succeeded.
	if _, err := agentloop.Enqueue(sprawlRoot, to, agentloop.Entry{
		ShortID: shortID,
		Class:   agentloop.ClassAsync,
		From:    agentName,
		Subject: subject,
		Body:    body,
	}); err != nil {
		fmt.Fprintf(deps.stderr, "warning: failed to enqueue harness entry for %s: %v\n", to, err)
	}

	fmt.Fprintf(deps.stderr, "Message sent to %s: %s\n", to, subject)
	return nil
}

var messagesReadCmd = &cobra.Command{
	Use:   "read <message-id>",
	Short: "Read a message by ID or prefix",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		msg, err := runMessagesRead(deps, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(deps.stderr, "ID: %s\nFrom: %s\nTo: %s\nSubject: %s\nTimestamp: %s\n\n%s\n", displayShortID(msg), msg.From, msg.To, msg.Subject, msg.Timestamp, msg.Body)
		archiveRef := msg.ID
		if msg.ShortID != "" {
			archiveRef = msg.ShortID
		}
		fmt.Fprintf(deps.stderr, "\nWhen done with this message, run `sprawl messages archive %s` to archive it.\n", archiveRef)
		return nil
	},
}

var messagesListCmd = &cobra.Command{
	Use:   "list [filter]",
	Short: "List messages with optional filter (all, unread, read, archived, sent)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		filter := ""
		if len(args) > 0 {
			filter = args[0]
		}
		return runMessagesListDisplay(deps, filter)
	},
}

var (
	archiveAll  bool
	archiveRead bool
)

var messagesArchiveCmd = &cobra.Command{
	Use:   "archive [message-id]",
	Short: "Archive a message or bulk archive messages",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deprecationWarning("messages archive", "messages_archive")
		deps := resolveMessagesDeps()

		if (archiveAll || archiveRead) && len(args) > 0 {
			return fmt.Errorf("cannot specify both --all and a message ID")
		}
		if !archiveAll && !archiveRead && len(args) == 0 {
			return fmt.Errorf("must specify a message ID, --all, or --read")
		}

		if archiveAll {
			return runMessagesArchiveAll(deps)
		}
		if archiveRead {
			return runMessagesArchiveRead(deps)
		}

		if err := runMessagesArchive(deps, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(deps.stderr, "Message archived\n")
		return nil
	},
}

func init() {
	messagesArchiveCmd.Flags().BoolVar(&archiveAll, "all", false, "Archive all messages in inbox")
	messagesArchiveCmd.Flags().BoolVar(&archiveRead, "read", false, "Archive only read messages")
}

var messagesUnreadCmd = &cobra.Command{
	Use:   "unread <message-id>",
	Short: "Mark a message as unread",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		if err := runMessagesUnread(deps, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Message marked as unread\n")
		return nil
	},
}

func runMessagesRead(deps *messagesDeps, msgID string) (*messages.Message, error) {
	deprecationWarning("messages read", "messages_read")
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return nil, err
	}

	fullID, err := messages.ResolvePrefix(sprawlRoot, agentName, msgID)
	if err != nil {
		return nil, err
	}

	return messages.ReadMessage(sprawlRoot, agentName, fullID)
}

func runMessagesList(deps *messagesDeps, filter string) ([]*messages.Message, error) {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return nil, err
	}

	return messages.List(sprawlRoot, agentName, filter)
}

func runMessagesArchive(deps *messagesDeps, msgID string) error {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return err
	}

	fullID, err := messages.ResolvePrefix(sprawlRoot, agentName, msgID)
	if err != nil {
		return err
	}

	return messages.Archive(sprawlRoot, agentName, fullID)
}

func runMessagesArchiveAll(deps *messagesDeps) error {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return err
	}
	count, err := messages.ArchiveAll(sprawlRoot, agentName)
	if count == 0 && err == nil {
		fmt.Fprintf(deps.stderr, "No messages to archive.\n")
		return nil
	}
	fmt.Fprintf(deps.stderr, "Archived %d messages.\n", count)
	return err
}

func runMessagesArchiveRead(deps *messagesDeps) error {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return err
	}
	count, err := messages.ArchiveRead(sprawlRoot, agentName)
	if count == 0 && err == nil {
		fmt.Fprintf(deps.stderr, "No messages to archive.\n")
		return nil
	}
	fmt.Fprintf(deps.stderr, "Archived %d messages.\n", count)
	return err
}

func runMessagesUnread(deps *messagesDeps, msgID string) error {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return err
	}

	fullID, err := messages.ResolvePrefix(sprawlRoot, agentName, msgID)
	if err != nil {
		return err
	}

	return messages.MarkUnread(sprawlRoot, agentName, fullID)
}

func runMessagesInbox(deps *messagesDeps) ([]*messages.Message, int, int, error) {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return nil, 0, 0, err
	}

	msgs, err := messages.Inbox(sprawlRoot, agentName)
	if err != nil {
		return nil, 0, 0, err
	}

	var newCount, readCount int
	for _, msg := range msgs {
		switch msg.Dir {
		case "new":
			newCount++
		case "cur":
			readCount++
		}
	}

	return msgs, newCount, readCount, nil
}

func runMessagesSent(deps *messagesDeps) ([]*messages.Message, error) {
	agentName, sprawlRoot, err := deps.resolveEnv()
	if err != nil {
		return nil, err
	}
	return messages.Sent(sprawlRoot, agentName)
}

func runMessagesSentDisplay(deps *messagesDeps) error {
	msgs, err := runMessagesSent(deps)
	if err != nil {
		return err
	}
	fmt.Fprintf(deps.stderr, "Sent: %d messages\n", len(msgs))
	formatSentTable(deps.stdout, msgs)
	return nil
}

func formatSentTable(w io.Writer, msgs []*messages.Message) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, msg := range msgs {
		ts := msg.Timestamp
		if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", displayShortID(msg), ts, msg.To, msg.Subject)
	}
	_ = tw.Flush()
}

// displayShortID returns the short display ID for a message.
// If ShortID is set, returns it. Otherwise returns first 6 chars of ID (or full ID if shorter).
// TODO(QUM-112): implement legacy fallback logic
func displayShortID(msg *messages.Message) string {
	if msg.ShortID != "" {
		return msg.ShortID
	}
	if len(msg.ID) > 6 {
		return msg.ID[:6]
	}
	return msg.ID
}

func runMessagesListDisplay(deps *messagesDeps, filter string) error {
	deprecationWarning("messages list", "messages_list")
	msgs, err := runMessagesList(deps, filter)
	if err != nil {
		return err
	}
	fmt.Fprintf(deps.stderr, "%d messages\n", len(msgs))
	formatInboxTable(deps.stdout, msgs)
	return nil
}

// runMessagesBroadcast sends a broadcast message to all active agents.
func runMessagesBroadcast(d *messagesDeps, subject, body string) error {
	agentName, sprawlRoot, err := d.resolveEnv()
	if err != nil {
		return err
	}

	count, err := messages.Broadcast(sprawlRoot, agentName, subject, body)
	if count > 0 {
		fmt.Fprintf(d.stderr, "Broadcast sent to %d agents: %s\n", count, subject)
	}
	if err != nil {
		return err
	}
	return nil
}
