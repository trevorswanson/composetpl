// Command composetpl renders docker-compose files from Helm-style chart templates
// and manages them as releases, the way Helm does for Kubernetes.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(exitCode(err))
	}
}

func newRootCmd() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:   "composetpl",
		Short: "Helm-style charts and releases for docker compose",
		Long: `composetpl renders docker-compose files from Helm-style charts (values.yaml,
_helpers.tpl, Sprig and Helm functions) and manages them as releases: install,
upgrade, rollback, history and uninstall, backed by docker compose.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	g.register(root)
	root.AddCommand(
		newTemplateCmd(g),
		newLintCmd(g),
		newInstallCmd(g),
		newUpgradeCmd(g),
		newRollbackCmd(g),
		newRepairCmd(g),
		newUninstallCmd(g),
		newListCmd(g),
		newStatusCmd(g),
		newHistoryCmd(g),
		newGetCmd(g),
		newDiffCmd(g),
		newTestCmd(g),
		newCreateCmd(),
		newShowCmd(g),
		newPackageCmd(),
		newPushCmd(g),
		newPullCmd(g),
		newDependencyCmd(g),
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
		},
	)
	return root
}

// exitError carries a specific process exit code (used by diff --detailed-exitcode).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

func exitCode(err error) int {
	if e, ok := err.(*exitError); ok {
		return e.code
	}
	return 1
}
