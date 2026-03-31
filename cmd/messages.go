package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/spf13/cobra"
)

// messagesDeps holds the dependencies for the messages commands, enabling testability.
type messagesDeps struct {
	getenv func(string) string
}

var defaultMessagesDeps *messagesDeps

func init() {
	messagesCmd.AddCommand(messagesSendCmd)
	messagesCmd.AddCommand(messagesInboxCmd)
	messagesCmd.AddCommand(messagesReadCmd)
	messagesCmd.AddCommand(messagesListCmd)
	messagesCmd.AddCommand(messagesArchiveCmd)
	messagesCmd.AddCommand(messagesUnreadCmd)
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
		msgs, newCount, readCount, err := runMessagesInbox(deps)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Inbox: %d new, %d read (%d total)\n", newCount, readCount, len(msgs))
		for _, msg := range msgs {
			fmt.Fprintf(os.Stderr, "  [%s] %s from %s: %s\n", msg.Dir, msg.Subject, msg.From, msg.Body)
		}
		return nil
	},
}

func resolveMessagesDeps() *messagesDeps {
	if defaultMessagesDeps != nil {
		return defaultMessagesDeps
	}
	return &messagesDeps{
		getenv: os.Getenv,
	}
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

	fmt.Fprintf(os.Stderr, "Message sent to %s: %s\n", to, subject)
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
