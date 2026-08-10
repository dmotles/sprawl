package state

import (
	"crypto/rand"
	"fmt"
)

// GenerateUUID creates a random UUID v4 string using crypto/rand.
//
// It lives in its own file rather than alongside any particular consumer
// because it belongs to none of them: the callers are the agentloop queue,
// spawn, the MCP server, rootinit and the supervisor runtime, and it is not
// about AgentState. QUM-1186 moved it here out of tasks.go, whose remaining
// contents were deleted with the task subsystem.
func GenerateUUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating UUID: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
