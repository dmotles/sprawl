//go:build !unix

package sigdump

import (
	"context"
	"log"
)

// Install is a no-op on non-unix platforms. SIGUSR1 does not exist on
// Windows, so there is no signal to listen for.
func Install(_ context.Context, _ string, _ *log.Logger) (stop func()) {
	return func() {}
}
