package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevorswanson/composetpl/internal/action"
	"github.com/trevorswanson/composetpl/internal/diff"
)

// diffFlags control how changes are printed.
type diffFlags struct {
	lines        int
	noColor      bool
	detailedExit bool
}

func (f *diffFlags) register(fs *pflag.FlagSet) {
	fs.IntVarP(&f.lines, "unified", "U", 3, "lines of context around each change")
	fs.BoolVar(&f.noColor, "no-color", os.Getenv("NO_COLOR") != "", "disable colour output [$NO_COLOR]")
	fs.BoolVar(&f.detailedExit, "detailed-exitcode", false, "exit with 2 when there are changes, 0 when there are none")
}

func (f *diffFlags) print(cmd *cobra.Command, release string, changes []diff.Change) error {
	out := cmd.OutOrStdout()
	color := !f.noColor && isTerminal(out)
	if err := diff.Print(out, changes, diff.PrintOptions{Release: release, Color: color, Context: f.lines}); err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "No changes.")
		return nil
	}
	if f.detailedExit {
		return &exitError{code: 2, err: fmt.Errorf("%d resource(s) changed", len(changes))}
	}
	return nil
}

func isTerminal(w interface{}) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func newDiffCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Preview changes, per service, network and volume",
		Long: `Preview changes to a release's compose manifest, like the helm-diff plugin.

  composetpl diff upgrade media ./stack --set apps.plex.enabled=false
  composetpl diff rollback media 3
  composetpl diff revision media 2 4`,
	}
	cmd.AddCommand(newDiffUpgradeCmd(g), newDiffRollbackCmd(g), newDiffRevisionCmd(g))
	return cmd
}

func newDiffUpgradeCmd(g *globalFlags) *cobra.Command {
	var (
		rf renderFlags
		df diffFlags
		up action.Upgrade
	)
	cmd := &cobra.Command{
		Use:   "upgrade NAME CHART",
		Short: "Show what an upgrade would change",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := rf.MergeValues()
			if err != nil {
				return err
			}
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			u := action.NewUpgrade(cfg)
			u.Strict, u.NoEnv, u.ChartVersion, u.SubchartNotes = rf.strict, rf.noEnv, rf.chartVersion, rf.subchartNotes
			u.ReuseValues, u.ResetValues, u.ResetThenReuseValues = up.ReuseValues, up.ResetValues, up.ResetThenReuseValues
			changes, err := action.DiffUpgrade(ctx(cmd), cfg, u, args[0], args[1], user)
			rf.printDebug(cmd.ErrOrStderr(), err)
			if err != nil {
				return err
			}
			return df.print(cmd, args[0], changes)
		},
	}
	rf.register(cmd.Flags())
	df.register(cmd.Flags())
	cmd.Flags().BoolVar(&up.ReuseValues, "reuse-values", false, "as for upgrade")
	cmd.Flags().BoolVar(&up.ResetValues, "reset-values", false, "as for upgrade")
	cmd.Flags().BoolVar(&up.ResetThenReuseValues, "reset-then-reuse-values", false, "as for upgrade")
	return cmd
}

func newDiffRollbackCmd(g *globalFlags) *cobra.Command {
	var df diffFlags
	cmd := &cobra.Command{
		Use:   "rollback NAME [REVISION]",
		Short: "Show what a rollback would change",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rev := 0
			if len(args) == 2 {
				var err error
				if rev, err = parseRevision(args[1]); err != nil {
					return err
				}
			}
			cfg, err := g.config(cmd, false)
			if err != nil {
				return err
			}
			changes, err := action.DiffRollback(cfg, args[0], rev)
			if err != nil {
				return err
			}
			return df.print(cmd, args[0], changes)
		},
	}
	df.register(cmd.Flags())
	return cmd
}

func newDiffRevisionCmd(g *globalFlags) *cobra.Command {
	var df diffFlags
	cmd := &cobra.Command{
		Use:   "revision NAME REVISION [REVISION]",
		Short: "Compare two revisions (the second defaults to the latest)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := parseRevision(args[1])
			if err != nil {
				return err
			}
			b := 0
			if len(args) == 3 {
				if b, err = parseRevision(args[2]); err != nil {
					return err
				}
			}
			cfg, err := g.config(cmd, false)
			if err != nil {
				return err
			}
			changes, err := action.DiffRevisions(cfg, args[0], a, b)
			if err != nil {
				return err
			}
			return df.print(cmd, args[0], changes)
		},
	}
	df.register(cmd.Flags())
	return cmd
}

func parseRevision(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid revision %q", s)
	}
	return n, nil
}
