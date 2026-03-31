package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dmotles/dendra/internal/messages"
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

var messagesInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Show messages in your inbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMessagesDeps()
		return runMessagesInboxDisplay(deps)
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
		fmt.Fprintf(w, "  %-4s  %-16s  %-12s  %s\n", status, ts, msg.From, msg.Subject)
	}
}

func runMessagesInboxDisplay(deps *messagesDeps) error {
	msgs, newCount, readCount, err := runMessagesInbox(deps)
	if err != nil {
		return err
	}
	fmt.Fprintf(deps.stderr, "Inbox: %d new, %d read (%d total)\n", newCount, readCount, len(msgs))
	formatInboxTable(deps.stdout, msgs)
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

	if err := messages.Send(dendraRoot, agentName, to, subject, body); err != nil {
		return err
	}

	fmt.Fprintf(deps.stderr, "Message sent to %s: %s\n", to, subject)
	return nil
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
