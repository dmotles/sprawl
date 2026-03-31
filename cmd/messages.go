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
