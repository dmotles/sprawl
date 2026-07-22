package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/spf13/cobra"
)

// hubConfigDeps injects the user-config-dir resolver and IO streams so the
// `hub url`/`hub token` set commands are testable without touching the real
// ~/.config/sprawl/config.yaml.
type hubConfigDeps struct {
	UserConfigDir func() (string, error)
	Stdin         io.Reader
	// Stderr carries status/hint output. These commands emit no data on
	// stdout (they persist config and confirm on stderr), so stdout is not
	// wired here.
	Stderr io.Writer
}

func resolveHubConfigDeps() *hubConfigDeps {
	return &hubConfigDeps{
		UserConfigDir: os.UserConfigDir,
		Stdin:         os.Stdin,
		Stderr:        os.Stderr,
	}
}

var hubURLCmd = &cobra.Command{
	Use:   "url",
	Short: "Manage the hub endpoint stored in user-level config",
}

var hubURLSetCmd = &cobra.Command{
	Use:   "set <url>",
	Short: "Set the hub endpoint URL in user-level config",
	Long: "Persist the hub endpoint URL to the user-level config file " +
		"(~/.config/sprawl/config.yaml). This is the third of four hub-URL " +
		"sources: flag > SPRAWL_HUB_URL env > this user config > project " +
		".sprawl/config.yaml.",
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runHubURLSet(resolveHubConfigDeps(), args[0])
	},
}

var hubTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage the hub bearer token stored in user-level config",
}

var hubTokenSetCmd = &cobra.Command{
	Use:   "set [<token>]",
	Short: "Set the hub bearer token in user-level config (arg or stdin)",
	Long: "Persist the host bearer token to the user-level config file " +
		"(~/.config/sprawl/config.yaml, mode 0600). Pass the token as an " +
		"argument, or pipe it on stdin (e.g. `echo \"$TOK\" | sprawl hub " +
		"token set`) to keep it out of the process argv. The token is never " +
		"printed or logged.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runHubTokenSet(resolveHubConfigDeps(), args)
	},
}

func init() {
	hubURLCmd.AddCommand(hubURLSetCmd)
	hubTokenCmd.AddCommand(hubTokenSetCmd)
	hubCmd.AddCommand(hubURLCmd, hubTokenCmd)
}

func runHubURLSet(deps *hubConfigDeps, rawURL string) error {
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return fmt.Errorf("empty hub URL; pass a non-empty URL, e.g. sprawl hub url set https://hub.example:443")
	}
	cfg, err := config.LoadUserConfig(deps.UserConfigDir)
	if err != nil {
		return fmt.Errorf("loading user config: %w", err)
	}
	cfg.HubURL = url
	if err := config.SaveUserConfig(deps.UserConfigDir, cfg); err != nil {
		return fmt.Errorf("saving user config: %w", err)
	}
	fmt.Fprintf(deps.Stderr, "Hub URL saved to user config: %s\n", url)
	fmt.Fprintf(deps.Stderr, "Next: set the token with `sprawl hub token set`, then start `sprawl enter` to register.\n")
	return nil
}

func runHubTokenSet(deps *hubConfigDeps, args []string) error {
	var token string
	if len(args) == 1 {
		token = strings.TrimSpace(args[0])
	} else {
		data, err := io.ReadAll(deps.Stdin)
		if err != nil {
			return fmt.Errorf("reading token from stdin: %w", err)
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return fmt.Errorf("no token provided; pass it as an argument or pipe it on stdin " +
			"(e.g. echo \"$TOK\" | sprawl hub token set)")
	}
	cfg, err := config.LoadUserConfig(deps.UserConfigDir)
	if err != nil {
		return fmt.Errorf("loading user config: %w", err)
	}
	cfg.HubToken = token
	if err := config.SaveUserConfig(deps.UserConfigDir, cfg); err != nil {
		return fmt.Errorf("saving user config: %w", err)
	}
	// The token value is deliberately never echoed.
	fmt.Fprintf(deps.Stderr, "Hub token saved to user config (mode 0600).\n")
	fmt.Fprintf(deps.Stderr, "Next: start `sprawl enter` to register with the hub.\n")
	return nil
}
