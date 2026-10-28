package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/trevorswanson/composetpl/pkg/action"
	"github.com/trevorswanson/composetpl/pkg/release"
)

// outputFormat is the -o table|json|yaml flag.
type outputFormat struct {
	format string
}

func (o *outputFormat) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.format, "output", "o", "table", "output format: table, json or yaml")
}

func (o *outputFormat) isTable() bool { return o.format == "table" }

// write prints v as JSON or YAML, or calls table for the human format.
func (o *outputFormat) write(w io.Writer, v interface{}, table func(io.Writer)) error {
	switch o.format {
	case "table":
		table(w)
		return nil
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "yaml":
		out, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
	return fmt.Errorf("unknown output format %q (want table, json or yaml)", o.format)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Mon Jan _2 15:04:05 2006")
}

func newListCmd(g *globalFlags) *cobra.Command {
	var (
		l                                                  action.List
		deployed, failed, pending, superseded, uninstalled bool
		short                                              bool
		format                                             outputFormat
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List releases",
		Long:    "List releases. By default only deployed and failed releases are shown.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewList(cfg)
			a.All, a.Filter, a.Selector = l.All, l.Filter, l.Selector
			for s, on := range map[release.Status]bool{
				release.StatusDeployed: deployed, release.StatusFailed: failed, release.StatusSuperseded: superseded,
				release.StatusUninstalled: uninstalled, release.StatusPendingInstall: pending,
				release.StatusPendingUpgrade: pending, release.StatusPendingRollback: pending,
			} {
				if on {
					a.Statuses = append(a.Statuses, s)
				}
			}
			items, err := a.Run(ctx(cmd))
			if err != nil {
				return err
			}
			if short {
				for _, it := range items {
					fmt.Fprintln(cmd.OutOrStdout(), it.Name)
				}
				return nil
			}
			if items == nil {
				items = []action.ListItem{}
			}
			return format.write(cmd.OutOrStdout(), items, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tREVISION\tUPDATED\tSTATUS\tCHART\tAPP VERSION\tCONTAINERS")
				for _, it := range items {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", it.Name, it.Revision, formatTime(it.LastDeployed),
						it.Status, it.Chart, it.Chart.AppVersion, it.Live)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().BoolVarP(&l.All, "all", "a", false, "show releases in every status")
	cmd.Flags().BoolVar(&deployed, "deployed", false, "show deployed releases")
	cmd.Flags().BoolVar(&failed, "failed", false, "show failed releases")
	cmd.Flags().BoolVar(&pending, "pending", false, "show pending releases")
	cmd.Flags().BoolVar(&superseded, "superseded", false, "show superseded releases")
	cmd.Flags().BoolVar(&uninstalled, "uninstalled", false, "show releases uninstalled with --keep-history")
	cmd.Flags().StringVarP(&l.Filter, "filter", "f", "", "regular expression; only show releases whose names match")
	cmd.Flags().StringVarP(&l.Selector, "selector", "l", "", "label selector: k=v, k!=v, k or !k, comma-separated (all must match)")
	cmd.Flags().BoolVarP(&short, "short", "q", false, "print release names only")
	format.register(cmd)
	return cmd
}

func newStatusCmd(g *globalFlags) *cobra.Command {
	var (
		revision int
		format   outputFormat
	)
	cmd := &cobra.Command{
		Use:   "status NAME",
		Short: "Show a release's status, containers and notes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewStatus(cfg)
			a.Revision = revision
			res, err := a.Run(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			return format.write(cmd.OutOrStdout(), res, func(w io.Writer) {
				r := res.Release
				fmt.Fprintf(w, "NAME: %s\nLAST DEPLOYED: %s\nSTATUS: %s\nREVISION: %d\nCHART: %s\n",
					r.Name, formatTime(r.LastDeployed), r.Status, r.Revision, r.Chart)
				if r.Description != "" {
					fmt.Fprintf(w, "DESCRIPTION: %s\n", r.Description)
				}
				if len(res.Containers) > 0 {
					fmt.Fprintln(w, "CONTAINERS:")
					tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
					fmt.Fprintln(tw, "  SERVICE\tCONTAINER\tSTATE\tHEALTH\tSTATUS")
					for _, c := range res.Containers {
						fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", c.Service, c.Name, c.State, c.Health, c.Status)
					}
					tw.Flush()
				}
				if strings.TrimSpace(r.Notes) != "" {
					fmt.Fprintf(w, "NOTES:\n%s\n", strings.TrimRight(r.Notes, "\n"))
				}
			})
		},
	}
	cmd.Flags().IntVar(&revision, "revision", 0, "show this revision instead of the latest")
	format.register(cmd)
	return cmd
}

func newHistoryCmd(g *globalFlags) *cobra.Command {
	var (
		max    int
		format outputFormat
	)
	cmd := &cobra.Command{
		Use:     "history NAME",
		Aliases: []string{"hist"},
		Short:   "Show a release's revisions",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, false)
			if err != nil {
				return err
			}
			h, err := action.History(cfg, args[0], max)
			if err != nil {
				return err
			}
			return format.write(cmd.OutOrStdout(), h, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
				fmt.Fprintln(tw, "REVISION\tUPDATED\tSTATUS\tCHART\tAPP VERSION\tDESCRIPTION")
				for _, r := range h {
					fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.Revision, formatTime(r.LastDeployed), r.Status,
						r.Chart, r.Chart.AppVersion, r.Description)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&max, "max", 256, "maximum number of revisions to show")
	format.register(cmd)
	return cmd
}

func newGetCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show stored details of a release revision",
	}
	type getter struct {
		use, short string
		print      func(w io.Writer, r *release.Release, all bool, format string) error
	}
	getters := []getter{
		{"values", "Show the user-supplied values (--all: computed values)", func(w io.Writer, r *release.Release, all bool, format string) error {
			v := r.Config
			if all {
				v = r.Values
			}
			if v == nil {
				v = map[string]interface{}{}
			}
			if format == "table" {
				format = "yaml"
			}
			return (&outputFormat{format: format}).write(w, v, nil)
		}},
		{"manifest", "Show the compose manifest", func(w io.Writer, r *release.Release, _ bool, _ string) error {
			_, err := io.WriteString(w, r.Manifest)
			return err
		}},
		{"notes", "Show the rendered NOTES.txt", func(w io.Writer, r *release.Release, _ bool, _ string) error {
			_, err := io.WriteString(w, r.Notes)
			return err
		}},
		{"hooks", "Show the hook services", func(w io.Writer, r *release.Release, _ bool, _ string) error {
			for _, h := range r.Hooks {
				fmt.Fprintf(w, "---\n# Hook: %s (%s, weight %d)\n%s", h.Name, strings.Join(h.Events, ","), h.Weight, h.Service)
			}
			return nil
		}},
		{"metadata", "Show the release metadata", func(w io.Writer, r *release.Release, _ bool, format string) error {
			md := map[string]interface{}{
				"name": r.Name, "revision": r.Revision, "status": r.Status, "chart": r.Chart.Name,
				"version": r.Chart.Version, "appVersion": r.Chart.AppVersion, "chartRef": r.Chart.Ref,
				"projectDirectory": r.ProjectDir, "firstDeployed": r.FirstDeployed, "lastDeployed": r.LastDeployed,
				"labels": r.Labels,
			}
			return (&outputFormat{format: format}).write(w, md, func(w io.Writer) {
				fmt.Fprintf(w, "NAME: %s\nCHART: %s\nVERSION: %s\nAPP_VERSION: %s\nREVISION: %d\nSTATUS: %s\nCHART REF: %s\nPROJECT DIRECTORY: %s\nDEPLOYED_AT: %s\n",
					r.Name, r.Chart.Name, r.Chart.Version, r.Chart.AppVersion, r.Revision, r.Status, r.Chart.Ref, r.ProjectDir, formatTime(r.LastDeployed))
				if len(r.Labels) > 0 {
					keys := make([]string, 0, len(r.Labels))
					for k := range r.Labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					fmt.Fprintln(w, "LABELS:")
					for _, k := range keys {
						fmt.Fprintf(w, "  %s=%s\n", k, r.Labels[k])
					}
				}
			})
		}},
		{"all", "Show everything", func(w io.Writer, r *release.Release, _ bool, format string) error {
			if format != "table" {
				return (&outputFormat{format: format}).write(w, r, nil)
			}
			printRelease(w, r, false)
			fmt.Fprintf(w, "CHART: %s\nUSER-SUPPLIED VALUES:\n", r.Chart)
			out, _ := yaml.Marshal(r.Config)
			fmt.Fprintf(w, "%s\nCOMPUTED VALUES:\n", out)
			out, _ = yaml.Marshal(r.Values)
			fmt.Fprintf(w, "%s\nMANIFEST:\n---\n%s", out, r.Manifest)
			return nil
		}},
	}
	for _, gt := range getters {
		gt := gt
		var (
			revision int
			all      bool
			format   outputFormat
		)
		sub := &cobra.Command{
			Use:   gt.use + " NAME",
			Short: gt.short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := g.config(cmd, false)
				if err != nil {
					return err
				}
				r, err := action.Get(cfg, args[0], revision)
				if err != nil {
					return err
				}
				return gt.print(cmd.OutOrStdout(), r, all, format.format)
			},
		}
		sub.Flags().IntVar(&revision, "revision", 0, "get this revision instead of the latest")
		switch gt.use {
		case "values":
			sub.Flags().BoolVarP(&all, "all", "a", false, "show all computed values")
			format.register(sub)
		case "metadata", "all":
			format.register(sub)
		}
		cmd.AddCommand(sub)
	}
	return cmd
}
