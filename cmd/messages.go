package cmd

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// messagesDeps holds the dependencies for the messages commands, enabling testability.
type messagesDeps struct {
	getenv     func(string) string
	stdout     io.Writer
	stderr     io.Writer
	tmuxRunner tmux.Runner
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
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesSend(deps, args[0], args[1], args[2])
	},
}

var inboxNewOnly bool

var messagesInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Show messages in your inbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesInboxDisplay(deps, inboxNewOnly)
	},
}

func init() {
	messagesInboxCmd.Flags().BoolVar(&inboxNewOnly, "new", false, "Show only unread (new) messages")
}

var messagesBroadcastCmd = &cobra.Command{
	Use:   "broadcast <subject> <body>",
	Short: "Broadcast a message to all active agents",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesBroadcast(deps, args[0], args[1])
	},
}

func resolveMessagesDeps() *messagesDeps {
	if defaultMessagesDeps != nil {
		return defaultMessagesDeps
	}
	deps := &messagesDeps{
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
	if tmuxPath, err := tmux.FindTmux(); err == nil {
		deps.tmuxRunner = &tmux.RealRunner{TmuxPath: tmuxPath}
	}
	return deps
}

func formatInboxTable(w io.Writer, msgs []*messages.Message) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, msg := range msgs {
		status := msg.Dir
		if msg.Dir == "new" {
			status = "NEW"
		} else if msg.Dir == "cur" {
			status = "read"
		}
		ts := msg.Timestamp
		if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", status, ts, msg.From, msg.Subject)
	}
	tw.Flush()
}

func runMessagesInboxDisplay(deps *messagesDeps, filterNew bool) error {
	msgs, newCount, readCount, err := runMessagesInbox(deps)
	if err != nil {
		return err
	}
	fmt.Fprintf(deps.stderr, "Inbox: %d new, %d read (%d total)\n", newCount, readCount, len(msgs))
	displayMsgs := msgs
	if filterNew {
		displayMsgs = nil
		for _, msg := range msgs {
			if msg.Dir == "new" {
				displayMsgs = append(displayMsgs, msg)
			}
		}
	}
	formatInboxTable(deps.stdout, displayMsgs)
	return nil
}

func runMessagesSend(deps *messagesDeps, to, subject, body string) error {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	var sendOpts []messages.SendOption
	if deps.tmuxRunner != nil {
		namespace := deps.getenv("DENDRA_NAMESPACE")
		if namespace == "" {
			namespace = state.ReadNamespace(dendraRoot)
		}
		if namespace == "" {
			namespace = tmux.DefaultNamespace
		}
		rootName := state.ReadRootName(dendraRoot)
		if rootName == "" {
			rootName = tmux.DefaultRootName
		}
		rootSession := tmux.RootSessionName(namespace, rootName)
		sendOpts = append(sendOpts, messages.WithNotify(func(from, subj string) {
			notification := fmt.Sprintf("[inbox] Message from %s: %s", from, subj)
			deps.tmuxRunner.SendKeys(rootSession, tmux.RootWindowName, notification)
		}))
	}

	if err := messages.Send(dendraRoot, agentName, to, subject, body, sendOpts...); err != nil {
		return err
	}

	fmt.Fprintf(deps.stderr, "Message sent to %s: %s\n", to, subject)
	return nil
}

var messagesReadCmd = &cobra.Command{
	Use:   "read <message-id>",
	Short: "Read a message by ID or prefix",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		msg, err := runMessagesRead(deps, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "From: %s\nTo: %s\nSubject: %s\nTimestamp: %s\n\n%s\n", msg.From, msg.To, msg.Subject, msg.Timestamp, msg.Body)
		return nil
	},
}

var messagesListCmd = &cobra.Command{
	Use:   "list [filter]",
	Short: "List messages with optional filter (all, unread, read, archived, sent)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		filter := ""
		if len(args) > 0 {
			filter = args[0]
		}
		msgs, err := runMessagesList(deps, filter)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			fmt.Fprintf(os.Stderr, "  [%s] %s from %s: %s\n", msg.Dir, msg.Subject, msg.From, msg.Body)
		}
		fmt.Fprintf(os.Stderr, "%d messages\n", len(msgs))
		return nil
	},
}

var messagesArchiveCmd = &cobra.Command{
	Use:   "archive <message-id>",
	Short: "Archive a message",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		if err := runMessagesArchive(deps, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Message archived\n")
		return nil
	},
}

var messagesUnreadCmd = &cobra.Command{
	Use:   "unread <message-id>",
	Short: "Mark a message as unread",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		if err := runMessagesUnread(deps, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Message marked as unread\n")
		return nil
	},
}

func runMessagesRead(deps *messagesDeps, msgID string) (*messages.Message, error) {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return nil, fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return nil, fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	fullID, err := messages.ResolvePrefix(dendraRoot, agentName, msgID)
	if err != nil {
		return nil, err
	}

	return messages.ReadMessage(dendraRoot, agentName, fullID)
}

func runMessagesList(deps *messagesDeps, filter string) ([]*messages.Message, error) {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return nil, fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return nil, fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	return messages.List(dendraRoot, agentName, filter)
}

func runMessagesArchive(deps *messagesDeps, msgID string) error {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	fullID, err := messages.ResolvePrefix(dendraRoot, agentName, msgID)
	if err != nil {
		return err
	}

	return messages.Archive(dendraRoot, agentName, fullID)
}

func runMessagesUnread(deps *messagesDeps, msgID string) error {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	fullID, err := messages.ResolvePrefix(dendraRoot, agentName, msgID)
	if err != nil {
		return err
	}

	return messages.MarkUnread(dendraRoot, agentName, fullID)
}

func runMessagesInbox(deps *messagesDeps) ([]*messages.Message, int, int, error) {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return nil, 0, 0, fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return nil, 0, 0, fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	msgs, err := messages.Inbox(dendraRoot, agentName)
	if err != nil {
		return nil, 0, 0, err
	}

	var newCount, readCount int
	for _, msg := range msgs {
		if msg.Dir == "new" {
			newCount++
		} else if msg.Dir == "cur" {
			readCount++
		}
	}

	return msgs, newCount, readCount, nil
}

// runMessagesBroadcast sends a broadcast message to all active agents.
func runMessagesBroadcast(d *messagesDeps, subject, body string) error {
	agentName := d.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := d.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	count, err := messages.Broadcast(dendraRoot, agentName, subject, body)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Broadcast sent to %d agents: %s\n", count, subject)
	return nil
}
