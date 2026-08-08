package cmd

import (
	"fmt"
	"os"
	"path/filepath"
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

func resolveVerifyDeps() *hooks.VerifyDeps {
	return &hooks.VerifyDeps{
		HooksPathOrigins: hooks.RealHooksPathOrigins,
		ResolvedHooksDir: hooks.RealResolvedHooksDir,
		CommonDir:        hooks.RealCommonDir,
		GitDir:           hooks.RealGitDir,
		TopLevel:         hooks.RealTopLevel,
		Getwd:            os.Getwd,
		Getenv:           os.Getenv,
		Lstat:            os.Lstat,
		Stat:             os.Stat,
		Readlink:         os.Readlink,
		EvalSymlinks:     filepath.EvalSymlinks,
		ReadFile:         os.ReadFile,
	}
}

var hooksVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Report whether the guard hooks are actually armed for this working tree",
	Long: "Resolve the whole guard chain for the current working tree and report what git " +
		"will actually run: every config scope that sets core.hooksPath (and which file set " +
		"it), the hooks directory git resolves, each hook point followed through any symlink to " +
		"its real path, its mode and executable bit, and whether a guard is genuinely reachable " +
		"from it.\n\n" +
		"This exists because git runs NO hooks and exits 0 when core.hooksPath names a path " +
		"that is not a populated directory — silently voiding the pre-commit main-commit " +
		"guard, the reference-transaction backstop, and the pre-commit validate gate at once. " +
		"A dangling symlink or a lost executable bit disarm the same way, and none of those " +
		"states is distinguishable from a healthy one without looking.\n\n" +
		"Exit codes are distinct on purpose: 0 armed, 1 disarmed, 2 the check itself could " +
		"not run. Collapsing 2 into 1 would make a crash indistinguishable from a detection, " +
		"and collapsing it into 0 would report an undetermined guard as a working one.",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		report, err := hooks.Verify(resolveVerifyDeps())
		// The report is this command's data product, so it goes to stdout —
		// unlike install/uninstall, whose output is progress commentary.
		hooks.PrintReport(os.Stdout, report)
		if err != nil {
			// UNKNOWN needs exit 2, which cobra cannot express: Execute maps
			// every RunE error to 1. The report (already flushed above) carries
			// the reason; stderr gets a short distinct line so a caller who
			// redirected stdout still learns the check did not run.
			fmt.Fprintln(os.Stderr, "hooks verify: could not determine whether the guard is armed — see the report; this is NOT a clean result")
			os.Exit(2)
		}
		if report.Verdict != hooks.VerdictArmed {
			return fmt.Errorf("guard hooks are not armed for this working tree; see the report above")
		}
		return nil
	},
}

func init() {
	hooksInstallCmd.Flags().StringVar(&hooksInstallBranch, "branch", "",
		"Protected branch (default: the repo's detected default branch)")
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksVerifyCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	rootCmd.AddCommand(hooksCmd)
}
