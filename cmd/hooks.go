package cmd

import (
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/hooks"
	"github.com/spf13/cobra"
)

// hookAssets carries the embedded guard script bodies, injected from package
// main via SetHookAssets (mirrors SetVersionInfo).
var hookAssets hooks.Assets

// SetHookAssets injects the go:embed'd canonical guard scripts so the hooks
// command is self-contained on any repo.
func SetHookAssets(a hooks.Assets) {
	hookAssets = a
}

var hooksInstallBranch string

func resolveHooksDeps() *hooks.Deps {
	return &hooks.Deps{
		HooksDir:     hooks.RealHooksDir,
		DetectBranch: hooks.RealDetectBranch,
		MkdirAll:     os.MkdirAll,
		ReadFile:     os.ReadFile,
		WriteFile:    hooks.RealWriteFileAtomic,
		Remove:       os.Remove,
		Now:          time.Now,
		Stderr:       os.Stderr,
	}
}

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage Sprawl's main-pollution guard git hooks",
	Long: "Install or remove the Sprawl main-pollution guards (the QUM-808 pre-commit " +
		"commit guard and the QUM-837 reference-transaction guard) on any repository. " +
		"The guards block non-root agents from committing or pushing to the protected " +
		"branch while leaving the root agent (weave) and human developers unaffected.",
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the main-pollution guard hooks into this repo",
	Long: "Install the Sprawl main-pollution guards into this repository's shared hooks " +
		"directory. Creates hook files where none exist, or chains a clearly-delimited " +
		"managed block onto existing hooks (never modifying your content). Idempotent — " +
		"safe to re-run. Records a manifest so `sprawl hooks uninstall` is surgical.",
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return hooks.Install(resolveHooksDeps(), hookAssets, hooksInstallBranch)
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the Sprawl-owned main-pollution guard hooks",
	Long: "Remove exactly what `sprawl hooks install` added: delete Sprawl-created hook " +
		"files and helpers, strip only the managed block from hooks it chained onto, and " +
		"remove the manifest. Idempotent and safe when nothing is installed.",
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return hooks.Uninstall(resolveHooksDeps())
	},
}

func init() {
	hooksInstallCmd.Flags().StringVar(&hooksInstallBranch, "branch", "",
		"Protected branch (default: the repo's detected default branch)")
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	rootCmd.AddCommand(hooksCmd)
}
