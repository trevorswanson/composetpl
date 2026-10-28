package main

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trevorswanson/composetpl/internal/action"
	"github.com/trevorswanson/composetpl/internal/chart"
)

func newCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create NAME",
		Short: "Create a new chart with the given name",
		Long: `Create a chart directory with Chart.yaml, values.yaml, values.schema.json,
.composetplignore, templates/_helpers.tpl, a service, NOTES.txt and a test hook.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := chart.Create(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Creating %s\n", dir)
			return nil
		},
	}
}

func newShowCmd(g *globalFlags) *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:     "show",
		Aliases: []string{"inspect"},
		Short:   "Show a chart's information (directory, .tgz or oci:// reference)",
	}
	cmd.PersistentFlags().StringVar(&version, "version", "", "chart version constraint for oci:// charts")
	load := func(cmd *cobra.Command, ref string) (*chart.Chart, error) {
		c, _, err := action.LoadChart(ctx(cmd), g.registry(cmd), ref, version)
		return c, err
	}
	sections := []struct {
		use, short string
		// parts are printed separated by "---" lines; empty parts are skipped.
		parts func(c *chart.Chart) [][]byte
	}{
		{"chart", "Show Chart.yaml", func(c *chart.Chart) [][]byte { return [][]byte{c.Raw["Chart.yaml"]} }},
		{"values", "Show values.yaml", func(c *chart.Chart) [][]byte { return [][]byte{c.Raw["values.yaml"]} }},
		{"readme", "Show the README", func(c *chart.Chart) [][]byte { return [][]byte{c.Readme()} }},
		{"schema", "Show values.schema.json", func(c *chart.Chart) [][]byte { return [][]byte{c.Schema} }},
		{"all", "Show Chart.yaml, values.yaml, the schema and the README", func(c *chart.Chart) [][]byte {
			return [][]byte{c.Raw["Chart.yaml"], c.Raw["values.yaml"], c.Schema, c.Readme()}
		}},
	}
	for _, s := range sections {
		s := s
		cmd.AddCommand(&cobra.Command{
			Use:   s.use + " CHART",
			Short: s.short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := load(cmd, args[0])
				if err != nil {
					return err
				}
				var out bytes.Buffer
				for _, part := range s.parts(c) {
					if len(part) == 0 {
						continue
					}
					if out.Len() > 0 {
						out.WriteString("---\n")
					}
					out.Write(part)
				}
				_, err = cmd.OutOrStdout().Write(out.Bytes())
				return err
			},
		})
	}
	return cmd
}

func newPackageCmd() *cobra.Command {
	var o chart.PackageOptions
	cmd := &cobra.Command{
		Use:   "package CHART_DIR...",
		Short: "Package chart directories into versioned .tgz archives",
		Long: `Package chart directories into <name>-<version>.tgz archives. file://
dependencies are vendored into the archive's charts/ directory, so the archive
is self-contained. Archives are reproducible: the same files always produce the
same bytes.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, dir := range args {
				out, err := chart.Package(dir, o)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Successfully packaged chart and saved it to: %s\n", out)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&o.Destination, "destination", "d", ".", "directory to write the archive to")
	cmd.Flags().StringVar(&o.Version, "version", "", "override the chart version")
	cmd.Flags().StringVar(&o.AppVersion, "app-version", "", "override the chart appVersion")
	return cmd
}
