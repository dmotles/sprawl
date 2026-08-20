package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/config"
)

// The process-wide Ledger.
//
// OpenProcessLedger is the ONE place that reads the world — the project config
// file, the environment, the secrets file, git — and turns it into a Ledger.
// Every one of those is an injected dependency, so all of its branching is
// testable without a repository, a database, or an environment. Process() on top
// of it is a singleton wrapper holding no logic.
//
// WHY A PROCESS SINGLETON AT ALL, since a global is normally the wrong answer
// here: the alternative is threading a *Ledger through SpawnDeps/RetireDeps and
// the supervisor, whose construction sites live in internal/supervisor/real.go —
// a file nine e2e matrix rows name explicitly. For a component that is off by
// default and whose failure mode is "record nothing", that validation bill buys
// nothing. The logic lives in the injectable function; the global is five lines.

// ProcessDeps follows the repo's deps-struct convention.
type ProcessDeps struct {
	SprawlRoot    string
	LoadConfig    func(sprawlRoot string) (*config.Config, error)
	Getenv        func(string) string
	UserConfigDir func() (string, error)
	Git           GitRunner
	Logger        *slog.Logger
	Now           func() time.Time
}

// OpenProcessLedger resolves configuration and opens the event log.
//
// Returns (nil, nil) when the feature flag is off — the default, and therefore
// the path taken on every invocation on every host that has never enabled the
// store. It does NO work in that case, deliberately: not even a git call.
func OpenProcessLedger(ctx context.Context, d ProcessDeps) (*Ledger, error) {
	cfg, err := d.LoadConfig(d.SprawlRoot)
	if err != nil {
		// Deliberately NOT read as "therefore disabled". A corrupt
		// .sprawl/config.yaml would then silently switch the store off on that
		// host alone — a partial-fleet outage with no symptom anywhere.
		return nil, fmt.Errorf("store: loading project config: %w", err)
	}
	if !cfg.EventLogEnabled() {
		return nil, nil
	}

	dsn, source, err := ResolveDSN(d.Getenv, d.UserConfigDir)
	if err != nil {
		return nil, err
	}

	remoteURL, err := originURL(ctx, d.Git, d.SprawlRoot)
	if err != nil {
		return nil, &HintError{
			Err:  fmt.Errorf("store: the event log is enabled but this repo's remote URL could not be read, and a project's identity IS its remote URL: %w", err),
			Hint: "add a remote (`git remote add origin <url>`), or switch the store off with `sprawl config set event_log.enabled false`",
		}
	}

	// HEAD is allowed to be missing, and that is the opposite call from the
	// remote URL above. A repository with no commits yet has no HEAD; that is a
	// legitimate state, the SHA only ANNOTATES events rather than identifying
	// anything, and refusing to record would mean the store cannot be enabled on
	// a fresh repo. So this degrades to an absent field.
	gitSHA, err := HeadSHA(ctx, d.Git, d.SprawlRoot)
	if err != nil {
		gitSHA = ""
	}

	return Open(ctx, LedgerConfig{
		Enabled:    true,
		DSN:        dsn,
		DSNSource:  source,
		RemoteURL:  remoteURL,
		GitSHA:     gitSHA,
		SprawlRoot: d.SprawlRoot,
		Logger:     d.Logger,
		Now:        d.Now,
	})
}

// originURL reads the repo's origin remote, which is a project's identity.
func originURL(ctx context.Context, run GitRunner, dir string) (string, error) {
	out, err := run(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", fmt.Errorf("remote.origin.url is empty")
	}
	return url, nil
}

var (
	processOnce   sync.Once
	processLedger *Ledger
	processErr    error
)

// Process returns the process-wide Ledger, opening it at most once.
//
// The (nil, nil) / (nil, err) distinction is the whole reason this returns an
// error at all, and callers must respect it:
//
//   - (nil, nil) means DISABLED. Telemetry emitters take the do-nothing path.
//   - (nil, err) means ENABLED BUT BROKEN — a missing DSN, an unreadable
//     config, a repo with no remote. A coordination operation must surface this;
//     swallowing it would silently drop a goal while the operator believes the
//     store is recording.
//
// An unreachable DATABASE is neither of those: it returns a degraded Ledger and
// a nil error, because telemetry still has to spill.
func Process(ctx context.Context, sprawlRoot string) (*Ledger, error) {
	processOnce.Do(func() {
		processLedger, processErr = OpenProcessLedger(ctx, ProcessDeps{
			SprawlRoot:    sprawlRoot,
			LoadConfig:    config.Load,
			Getenv:        os.Getenv,
			UserConfigDir: os.UserConfigDir,
			Git:           RealGit,
			Logger:        slog.Default(),
		})
	})
	return processLedger, processErr
}
