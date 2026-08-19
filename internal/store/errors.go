package store

import (
	"errors"
	"fmt"
)

var (
	// ErrSchemaViolation means a payload did not satisfy the schema its caller
	// pinned. Callers distinguish it from a transport failure because the two
	// have OPPOSITE remedies: a violation is a bug in the emitter and must
	// never be retried or spilled, while a transport failure is transient and
	// spillable.
	ErrSchemaViolation = errors.New("store: payload violates its pinned event-type schema")

	// ErrUnsupportedSchemaKeyword means the schema ITSELF uses a construct this
	// validator cannot enforce. It is deliberately an ERROR rather than an
	// ignore: silently skipping a keyword ships a constraint that reads as
	// enforced and is not, and nothing downstream would ever reveal it.
	ErrUnsupportedSchemaKeyword = errors.New("store: event-type schema uses a keyword this validator cannot enforce")

	// ErrInsecureSecrets means the secrets file is group- or world-accessible.
	ErrInsecureSecrets = errors.New("store: secrets file is group- or world-accessible")

	// ErrDegraded means the event log is unreachable and the operation is one
	// that must NOT proceed locally. Only telemetry and lifecycle events spill;
	// anything carrying cross-host coordination meaning fails with this.
	ErrDegraded = errors.New("store: event log is unreachable")

	// ErrNoOpenContract means a close event referenced something that is not an
	// open contract — already closed, or never opened. Closes are final and the
	// log is monotone, so this is refused rather than absorbed.
	ErrNoOpenContract = errors.New("store: no such open contract")
)

// HintError pairs a failure with the single next action that resolves it.
//
// Per /cli-ux-best-practices the primary consumer of this CLI is an agent, so an
// error that does not say what to do next costs a round trip at best and an
// invented remedy at worst.
type HintError struct {
	Err  error
	Hint string
}

func (e *HintError) Error() string {
	if e.Hint == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%v\nnext: %s", e.Err, e.Hint)
}

func (e *HintError) Unwrap() error { return e.Err }

func isSchemaViolation(err error) bool { return errors.Is(err, ErrSchemaViolation) }
