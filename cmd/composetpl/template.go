package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"composetpl/internal/action"
	"composetpl/internal/lint"
	"composetpl/internal/output"
)

// templateFlags are shared by template and lint.
type templateFlags struct {
	renderFlags
	name     string
	showOnly []string
}

func (f *templateFlags) register(cmd *cobra.Command) {
	f.renderFlags.register(cmd.Flags())
	cmd.Flags().StringVar(&f.name, "name", "", "release name (.Release.Name and the compose project name; default: chart name)")
	cmd.Flags().StringArrayVarP(&f.showOnly, "show-only", "s", nil, "only render the given template, e.g. templates/apps.yaml (repeatable)")
}

func (f *templateFlags) render(chartPath string) (*action.Rendered, error) {
	user, err := f.MergeValues()
	if err != nil {
		return nil, err
	}
	return action.Render(action.RenderOptions{
		ChartPath: chartPath, ReleaseName: f.name, UserValues: user,
		Strict: f.strict, NoEnv: f.noEnv, ShowOnly: f.showOnly,
	})
}

func (f *templateFlags) releaseName(r *action.Rendered) string {
	if f.name != "" {
		return f.name
	}
	return r.Chart.Metadata.Name
}

func newTemplateCmd() *cobra.Command {
	var (
		f         templateFlags
		outFile   string
		outputDir string
	)
	cmd := &cobra.Command{
		Use:   "template CHART",
		Short: "Render a chart to a docker-compose file",
		Long: `Render a chart to a docker-compose file.

By default every template is deep-merged into a single compose document written
to stdout (or --output). With --output-dir each template is written to its own file.

  composetpl template ./stack | docker compose -f - up -d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outFile != "" && outputDir != "" {
				return errors.New("--output and --output-dir are mutually exclusive")
			}
			r, err := f.render(args[0])
			if err != nil {
				return err
			}
			if outputDir != "" {
				written, err := output.WriteSplit(outputDir, r.Templates)
				f.printDebug(cmd.ErrOrStderr(), err)
				for _, p := range written {
					fmt.Fprintln(cmd.ErrOrStderr(), "wrote", p)
				}
				return err
			}
			root, err := r.Merge()
			if err != nil {
				f.printDebug(cmd.ErrOrStderr(), err)
				return err
			}
			output.SetDefaultName(root, f.releaseName(r))
			doc, err := output.Encode(root)
			if err != nil {
				return err
			}
			if outFile != "" {
				return os.WriteFile(outFile, doc, 0o644)
			}
			_, err = cmd.OutOrStdout().Write(doc)
			return err
		},
	}
	f.register(cmd)
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "write the merged compose file here instead of stdout")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "write one file per template into this directory")
	return cmd
}

func newLintCmd() *cobra.Command {
	var f templateFlags
	cmd := &cobra.Command{
		Use:   "lint CHART",
		Short: "Render a chart and check the result is a plausible compose file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := f.render(args[0])
			if err != nil {
				return err
			}
			root, err := r.Merge()
			if err != nil {
				f.printDebug(cmd.ErrOrStderr(), err)
				return err
			}
			name := r.Chart.Metadata.Name
			output.SetDefaultName(root, f.releaseName(r))
			res := lint.Check(root)
			out := cmd.OutOrStdout()
			for _, w := range res.Warnings {
				fmt.Fprintln(out, "[WARNING]", w)
			}
			for _, e := range res.Errors {
				fmt.Fprintln(out, "[ERROR]", e)
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("%s: %d error(s)", name, len(res.Errors))
			}
			doc, err := output.Encode(root)
			if err != nil {
				return err
			}
			ran, err := lint.DockerCompose(doc)
			if err != nil {
				return err
			}
			if !ran {
				fmt.Fprintln(out, "[INFO] docker compose not found; skipped `docker compose config` validation")
			}
			fmt.Fprintf(out, "%s: lint passed\n", name)
			return nil
		},
	}
	f.register(cmd)
	return cmd
}
