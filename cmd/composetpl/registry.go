package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"composetpl/internal/action"
	"composetpl/internal/chart"
)

func newPushCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "push CHART.tgz oci://REGISTRY/PATH",
		Short: "Push a packaged chart to an OCI registry",
		Long: `Push a packaged chart to oci://REGISTRY/PATH/<chart name>:<chart version>,
the same layout Helm uses. Log in first with docker login; composetpl reads the
Docker credential store.

  composetpl package ./stack
  composetpl push media-0.2.0.tgz oci://ghcr.io/me/charts`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			ref, digest, err := g.registry(cmd).Push(ctx(cmd), data, args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushed: %s\nDigest: %s\n", ref, digest)
			return nil
		},
	}
}

func newPullCmd(g *globalFlags) *cobra.Command {
	var (
		version string
		dest    string
		untar   bool
	)
	cmd := &cobra.Command{
		Use:   "pull oci://REGISTRY/PATH/NAME[:TAG]",
		Short: "Download a chart from an OCI registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.registry(cmd).Pull(ctx(cmd), args[0], version)
			if err != nil {
				return err
			}
			c, err := chart.LoadArchive(p.Archive, chart.Options{})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Pulled: %s\nDigest: %s\n", p.Ref, p.Digest)
			if !untar {
				path := filepath.Join(dest, fmt.Sprintf("%s-%s.tgz", c.Metadata.Name, c.Metadata.Version))
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return err
				}
				fmt.Fprintf(out, "Saved: %s\n", path)
				return os.WriteFile(path, p.Archive, 0o644)
			}
			files, err := chart.ReadArchive(p.Archive)
			if err != nil {
				return err
			}
			dir := filepath.Join(dest, c.Metadata.Name)
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("%s already exists", dir)
			}
			for rel, data := range files {
				p := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, data, 0o644); err != nil {
					return err
				}
			}
			fmt.Fprintf(out, "Saved: %s\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "version constraint, e.g. ^1.2.0 (default: latest; or put a tag in the reference)")
	cmd.Flags().StringVarP(&dest, "destination", "d", ".", "directory to save the chart in")
	cmd.Flags().BoolVar(&untar, "untar", false, "unpack the chart into DESTINATION/<name>")
	return cmd
}

func newDependencyCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dependency",
		Aliases: []string{"dep", "dependencies"},
		Short:   "Manage a chart's dependencies (charts/ and Chart.lock)",
		Long: `Manage a chart's dependencies. Chart.yaml lists them with a file:// path or an
oci:// repository and a semver constraint:

  dependencies:
    - name: common
      version: ^0.1.0
      repository: oci://ghcr.io/me/charts

update resolves the newest matching versions, vendors them into charts/ and
writes Chart.lock; build restores exactly what Chart.lock records.`,
	}
	deps := func(cmd *cobra.Command) *action.Dependencies {
		return &action.Dependencies{Registry: g.registry(cmd), Log: cmd.ErrOrStderr()}
	}
	var format outputFormat
	list := &cobra.Command{
		Use:   "list CHART_DIR",
		Short: "List the dependencies and whether they are available",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := deps(cmd).List(args[0])
			if err != nil {
				return err
			}
			return format.write(cmd.OutOrStdout(), rows, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tVERSION\tREPOSITORY\tSTATUS")
				for _, r := range rows {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.Version, r.Repository, r.Status)
				}
				tw.Flush()
			})
		},
	}
	format.register(list)
	update := &cobra.Command{
		Use:     "update CHART_DIR",
		Aliases: []string{"up"},
		Short:   "Resolve dependencies, vendor them into charts/ and write Chart.lock",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := deps(cmd).Update(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %d dependencies and %s\n", len(lock.Dependencies), chart.LockFile)
			return nil
		},
	}
	build := &cobra.Command{
		Use:   "build CHART_DIR",
		Short: "Vendor the exact versions recorded in Chart.lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := deps(cmd).Build(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %d dependencies from %s\n", len(lock.Dependencies), chart.LockFile)
			return nil
		},
	}
	cmd.AddCommand(list, update, build)
	return cmd
}
