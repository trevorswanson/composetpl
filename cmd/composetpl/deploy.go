package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevorswanson/composetpl/pkg/action"
	"github.com/trevorswanson/composetpl/pkg/release"
)

func newInstallCmd(g *globalFlags) *cobra.Command {
	var (
		rf     renderFlags
		df     deployFlags
		in     action.Install
		format outputFormat
	)
	cmd := &cobra.Command{
		Use:   "install NAME CHART",
		Short: "Install a chart as a new release",
		Long: `Install a chart as a new release: render it, then run
docker compose -p NAME up -d --remove-orphans, and record revision 1.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := rf.MergeValues()
			if err != nil {
				return err
			}
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewInstall(cfg)
			a.DeployOptions, a.Strict, a.NoEnv = df.DeployOptions, rf.strict, rf.noEnv
			a.Atomic, a.DryRun, a.Description, a.ProjectDir = in.Atomic, in.DryRun, in.Description, in.ProjectDir
			a.ChartVersion, a.SubchartNotes, a.Labels = rf.chartVersion, rf.subchartNotes, in.Labels
			rel, err := a.Run(ctx(cmd), args[0], args[1], user)
			rf.printDebug(cmd.ErrOrStderr(), err)
			if err != nil {
				return err
			}
			return format.write(cmd.OutOrStdout(), rel, func(w io.Writer) { printRelease(w, rel, in.DryRun) })
		},
	}
	rf.register(cmd.Flags())
	df.register(cmd.Flags())
	cmd.Flags().BoolVar(&in.Atomic, "atomic", false, "if the install fails, tear it down and delete the release")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "render and validate with docker compose config, without deploying")
	cmd.Flags().StringVar(&in.Description, "description", "", "custom description for the revision")
	cmd.Flags().StringVar(&in.ProjectDir, "project-directory", "", "compose project directory for relative paths (default: the chart directory)")
	cmd.Flags().StringToStringVar(&in.Labels, "labels", nil, "labels to record on the release, e.g. team=media,tier=apps")
	format.register(cmd)
	return cmd
}

func newUpgradeCmd(g *globalFlags) *cobra.Command {
	var (
		rf     renderFlags
		df     deployFlags
		up     action.Upgrade
		format outputFormat
	)
	cmd := &cobra.Command{
		Use:   "upgrade NAME CHART",
		Short: "Upgrade a release to a new chart or values",
		Long: `Upgrade a release: render the chart, run docker compose up, and record a new
revision. Compose recreates only containers whose configuration changed.

With no -f/--set flags the previous revision's values are reused, as in Helm.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := rf.MergeValues()
			if err != nil {
				return err
			}
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewUpgrade(cfg)
			a.DeployOptions, a.Strict, a.NoEnv = df.DeployOptions, rf.strict, rf.noEnv
			a.Install, a.ReuseValues, a.ResetValues, a.ResetThenReuseValues = up.Install, up.ReuseValues, up.ResetValues, up.ResetThenReuseValues
			a.HistoryMax, a.Atomic, a.DryRun, a.Description, a.ProjectDir = up.HistoryMax, up.Atomic, up.DryRun, up.Description, up.ProjectDir
			a.ChartVersion, a.SubchartNotes, a.Labels = rf.chartVersion, rf.subchartNotes, up.Labels
			warnManaged(cmd, cfg, args[0])
			rel, err := a.Run(ctx(cmd), args[0], args[1], user)
			rf.printDebug(cmd.ErrOrStderr(), err)
			if err != nil {
				return err
			}
			if !up.DryRun && format.isTable() {
				fmt.Fprintf(cmd.OutOrStdout(), "Release %q has been upgraded.\n", rel.Name)
			}
			return format.write(cmd.OutOrStdout(), rel, func(w io.Writer) { printRelease(w, rel, up.DryRun) })
		},
	}
	rf.register(cmd.Flags())
	df.register(cmd.Flags())
	cmd.Flags().BoolVarP(&up.Install, "install", "i", false, "install the release if it does not exist")
	cmd.Flags().BoolVar(&up.ReuseValues, "reuse-values", false, "reuse the last revision's values (and chart defaults), merging in -f/--set")
	cmd.Flags().BoolVar(&up.ResetValues, "reset-values", false, "ignore the last revision's values; use the chart defaults plus -f/--set")
	cmd.Flags().BoolVar(&up.ResetThenReuseValues, "reset-then-reuse-values", false, "use the new chart's defaults, then the last revision's values, then -f/--set")
	cmd.Flags().IntVar(&up.HistoryMax, "history-max", 10, "maximum revisions kept per release (0 = unlimited)")
	cmd.Flags().BoolVar(&up.Atomic, "atomic", false, "if the upgrade fails, roll back to the last deployed revision")
	cmd.Flags().BoolVar(&up.DryRun, "dry-run", false, "render and validate with docker compose config, without deploying")
	cmd.Flags().StringVar(&up.Description, "description", "", "custom description for the revision")
	cmd.Flags().StringVar(&up.ProjectDir, "project-directory", "", "compose project directory for relative paths (default: the chart directory)")
	cmd.Flags().StringToStringVar(&up.Labels, "labels", nil, "labels to merge over the release's current labels; an empty value removes one")
	format.register(cmd)
	return cmd
}

func newRollbackCmd(g *globalFlags) *cobra.Command {
	var (
		df         deployFlags
		historyMax int
		dryRun     bool
		labels     map[string]string
	)
	cmd := &cobra.Command{
		Use:   "rollback NAME [REVISION]",
		Short: "Roll a release back to a previous revision",
		Long: `Roll a release back by redeploying an earlier revision's manifest as a new
revision. With no REVISION, rolls back to the previous one. Rollback also
recovers a release stuck in a pending state.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewRollback(cfg)
			a.DeployOptions, a.HistoryMax, a.DryRun, a.Labels = df.DeployOptions, historyMax, dryRun, labels
			warnManaged(cmd, cfg, args[0])
			if len(args) == 2 {
				if a.Version, err = parseRevision(args[1]); err != nil {
					return err
				}
			}
			rel, err := a.Run(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "Would roll %q back to revision %s.\n", rel.Name, strings.TrimPrefix(rel.Description, "Rollback to "))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rollback was a success! %q is now at revision %d.\n", rel.Name, rel.Revision)
			return nil
		},
	}
	df.register(cmd.Flags())
	cmd.Flags().IntVar(&historyMax, "history-max", 10, "maximum revisions kept per release (0 = unlimited)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without deploying")
	cmd.Flags().StringToStringVar(&labels, "labels", nil, "labels to merge over the target revision's labels; an empty value removes one")
	return cmd
}

// repairResult is what `repair -o json|yaml` prints.
type repairResult struct {
	Release  string                `json:"release"`
	Revision int                   `json:"revision"`
	Drift    []action.ServiceDrift `json:"drift"`
	Repaired bool                  `json:"repaired"`
}

func newRepairCmd(g *globalFlags) *cobra.Command {
	var (
		df     deployFlags
		dryRun bool
		format outputFormat
	)
	cmd := &cobra.Command{
		Use:   "repair NAME",
		Short: "Bring a release's containers back to its deployed revision",
		Long: `Repair compares a release's containers with its last deployed revision and, if
any were removed, stopped or changed outside composetpl, runs docker compose up
on that revision's manifest again. Unlike rollback it records no new revision
and runs no hooks. --dry-run only reports the drift.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewRepair(cfg)
			a.DeployOptions, a.DryRun = df.DeployOptions, dryRun
			drift, err := a.Drift(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			res := repairResult{Release: args[0], Drift: drift}
			if len(drift) == 0 && !df.ForceRecreate {
				a.DryRun = true
			}
			rel, err := a.Run(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			res.Revision, res.Repaired = rel.Revision, !a.DryRun
			return format.write(cmd.OutOrStdout(), res, func(w io.Writer) {
				if len(drift) == 0 {
					fmt.Fprintf(w, "Release %q matches revision %d.\n", rel.Name, rel.Revision)
				} else {
					tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
					fmt.Fprintln(tw, "SERVICE\tDRIFT\tCONTAINERS")
					for _, d := range drift {
						fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Service, d.Reason, strings.Join(d.Containers, ","))
					}
					_ = tw.Flush()
				}
				if res.Repaired {
					fmt.Fprintf(w, "Release %q has been repaired to revision %d.\n", rel.Name, rel.Revision)
				}
			})
		},
	}
	df.registerUp(cmd.Flags())
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report drift without changing anything")
	format.register(cmd)
	return cmd
}

func newUninstallCmd(g *globalFlags) *cobra.Command {
	var u action.Uninstall
	cmd := &cobra.Command{
		Use:     "uninstall NAME...",
		Aliases: []string{"delete", "un"},
		Short:   "Uninstall releases",
		Long: `Uninstall releases with docker compose down --remove-orphans. Named volumes are
kept unless --volumes is given.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			for _, name := range args {
				warnManaged(cmd, cfg, name)
				a := action.NewUninstall(cfg)
				a.KeepHistory, a.Volumes, a.RemoveImages, a.DryRun = u.KeepHistory, u.Volumes, u.RemoveImages, u.DryRun
				a.NoHooks, a.Timeout = u.NoHooks, u.Timeout
				if _, err := a.Run(ctx(cmd), name); err != nil {
					return err
				}
				if u.DryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "release %q would be uninstalled\n", name)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "release %q uninstalled\n", name)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&u.KeepHistory, "keep-history", false, "keep the release history and mark the release uninstalled")
	cmd.Flags().BoolVar(&u.Volumes, "volumes", false, "also remove named volumes declared by the release")
	cmd.Flags().StringVar(&u.RemoveImages, "rmi", "", "also remove images: local or all")
	cmd.Flags().BoolVar(&u.DryRun, "dry-run", false, "show what would be uninstalled")
	cmd.Flags().BoolVar(&u.NoHooks, "no-hooks", false, "skip pre-delete and post-delete hooks")
	cmd.Flags().DurationVar(&u.Timeout, "timeout", 5*time.Minute, "how long each delete hook may run")
	return cmd
}

// warnManaged warns when another tool (release.LabelManagedBy) manages release
// name, since it may revert or fight changes made with the CLI.
func warnManaged(cmd *cobra.Command, cfg *action.Configuration, name string) {
	rel, err := action.Get(cfg, name, 0)
	if err != nil {
		return
	}
	if by := rel.Labels[release.LabelManagedBy]; by != "" && by != "composetpl" {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: release %q is managed by %s, which may revert changes made here\n", name, by)
	}
}

// printRelease prints the summary Helm shows after install/upgrade.
func printRelease(w io.Writer, r *release.Release, dryRun bool) {
	fmt.Fprintf(w, "NAME: %s\n", r.Name)
	fmt.Fprintf(w, "LAST DEPLOYED: %s\n", formatTime(r.LastDeployed))
	fmt.Fprintf(w, "STATUS: %s\n", r.Status)
	fmt.Fprintf(w, "REVISION: %d\n", r.Revision)
	if r.Description != "" {
		fmt.Fprintf(w, "DESCRIPTION: %s\n", r.Description)
	}
	if dryRun {
		fmt.Fprintf(w, "MANIFEST:\n---\n%s", r.Manifest)
	}
	if strings.TrimSpace(r.Notes) != "" {
		fmt.Fprintf(w, "NOTES:\n%s\n", strings.TrimRight(r.Notes, "\n"))
	}
}
