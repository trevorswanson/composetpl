// Command composetpl renders docker-compose files from Helm-style chart templates.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"composetpl/internal/chart"
	"composetpl/internal/engine"
	"composetpl/internal/lint"
	"composetpl/internal/output"
	"composetpl/internal/values"
)

var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "composetpl",
		Short:         "Render docker-compose files from Helm-style chart templates",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTemplateCmd(), newLintCmd(), &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
	})
	return root
}

// renderFlags are shared by every command that renders a chart.
type renderFlags struct {
	values.Options
	name     string
	strict   bool
	noEnv    bool
	debug    bool
	showOnly []string
}

func (f *renderFlags) register(fs *pflag.FlagSet) {
	fs.StringArrayVarP(&f.ValueFiles, "values", "f", nil, "values file to merge over values.yaml (repeatable, later wins)")
	fs.StringArrayVar(&f.Values, "set", nil, "set a value, e.g. apps.plex.enabled=false (repeatable)")
	fs.StringArrayVar(&f.StringValues, "set-string", nil, "set a value, always as a string (repeatable)")
	fs.StringArrayVar(&f.FileValues, "set-file", nil, "set a value to a file's contents, e.g. key=path (repeatable)")
	fs.StringVar(&f.name, "name", "", "release name (.Release.Name and the compose project name; default: chart name)")
	fs.BoolVar(&f.strict, "strict", false, "fail on references to missing values")
	fs.BoolVar(&f.noEnv, "no-env", false, "disable the env and expandenv template functions")
	fs.BoolVar(&f.debug, "debug", false, "print the raw rendered output when it is not valid YAML")
	fs.StringArrayVarP(&f.showOnly, "show-only", "s", nil, "only render the given template, e.g. templates/apps.yaml (repeatable)")
}

func (f *renderFlags) render(dir string) (*chart.Chart, []engine.Rendered, error) {
	c, err := chart.Load(dir)
	if err != nil {
		return nil, nil, err
	}
	user, err := f.MergeValues()
	if err != nil {
		return nil, nil, err
	}
	vals := values.Coalesce(user, c.Values)

	name := f.name
	if name == "" {
		name = c.Metadata.Name
	}
	release := map[string]interface{}{"Name": name, "Service": "composetpl"}

	rendered, err := engine.Render(c, vals, release, engine.Options{Strict: f.strict, NoEnv: f.noEnv})
	if err != nil {
		return nil, nil, err
	}
	if len(f.showOnly) > 0 {
		rendered, err = filterShowOnly(c.Metadata.Name, rendered, f.showOnly)
		if err != nil {
			return nil, nil, err
		}
	}
	return c, rendered, nil
}

func filterShowOnly(chartName string, rendered []engine.Rendered, only []string) ([]engine.Rendered, error) {
	var out []engine.Rendered
	for _, want := range only {
		full := chartName + "/" + strings.TrimPrefix(want, "./")
		found := false
		for _, r := range rendered {
			if r.Name == full {
				out = append(out, r)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("could not find template %s in chart", want)
		}
	}
	return out, nil
}

// merge parses and merges the rendered templates, printing the offending raw
// output on a YAML error when --debug is set.
func (f *renderFlags) merge(rendered []engine.Rendered) (*yaml.Node, error) {
	root, err := output.Merge(rendered)
	f.printDebug(err)
	return root, err
}

func (f *renderFlags) printDebug(err error) {
	var pe *output.ParseError
	if !f.debug || !errors.As(err, &pe) {
		return
	}
	fmt.Fprintf(os.Stderr, "--- raw output of %s ---\n", pe.Template)
	sc := bufio.NewScanner(strings.NewReader(pe.Content))
	for n := 1; sc.Scan(); n++ {
		fmt.Fprintf(os.Stderr, "%4d | %s\n", n, sc.Text())
	}
	fmt.Fprintln(os.Stderr, "---")
}

func newTemplateCmd() *cobra.Command {
	var (
		f         renderFlags
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
			c, rendered, err := f.render(args[0])
			if err != nil {
				return err
			}
			if outputDir != "" {
				written, err := output.WriteSplit(outputDir, rendered)
				f.printDebug(err)
				for _, p := range written {
					fmt.Fprintln(cmd.ErrOrStderr(), "wrote", p)
				}
				return err
			}
			root, err := f.merge(rendered)
			if err != nil {
				return err
			}
			output.SetDefaultName(root, releaseName(f, c))
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
	f.register(cmd.Flags())
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "write the merged compose file here instead of stdout")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "write one file per template into this directory")
	return cmd
}

func newLintCmd() *cobra.Command {
	var f renderFlags
	cmd := &cobra.Command{
		Use:   "lint CHART",
		Short: "Render a chart and check the result is a plausible compose file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, rendered, err := f.render(args[0])
			if err != nil {
				return err
			}
			root, err := f.merge(rendered)
			if err != nil {
				return err
			}
			output.SetDefaultName(root, releaseName(f, c))
			res := lint.Check(root)
			out := cmd.OutOrStdout()
			for _, w := range res.Warnings {
				fmt.Fprintln(out, "[WARNING]", w)
			}
			for _, e := range res.Errors {
				fmt.Fprintln(out, "[ERROR]", e)
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("%s: %d error(s)", c.Metadata.Name, len(res.Errors))
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
				fmt.Fprintln(out, "[INFO] docker not found; skipped `docker compose config` validation")
			}
			fmt.Fprintf(out, "%s: lint passed\n", c.Metadata.Name)
			return nil
		},
	}
	f.register(cmd.Flags())
	return cmd
}

func releaseName(f renderFlags, c *chart.Chart) string {
	if f.name != "" {
		return f.name
	}
	return c.Metadata.Name
}
