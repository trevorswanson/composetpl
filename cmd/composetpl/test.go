package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevorswanson/composetpl/internal/action"
)

func newTestCmd(g *globalFlags) *cobra.Command {
	var (
		t      action.ReleaseTesting
		logs   bool
		format outputFormat
	)
	cmd := &cobra.Command{
		Use:   "test NAME",
		Short: "Run a release's test hooks",
		Long: `Run the services of the deployed revision whose x-composetpl hook is "test",
one at a time with docker compose run, like helm test. Stops at the first failure.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.config(cmd, true)
			if err != nil {
				return err
			}
			a := action.NewReleaseTesting(cfg)
			a.Timeout, a.Filter = t.Timeout, t.Filter
			rel, results, runErr := a.Run(ctx(cmd), args[0])
			if rel == nil {
				return runErr
			}
			if !logs {
				for i := range results {
					results[i].Logs = ""
				}
			}
			err = format.write(cmd.OutOrStdout(), results, func(w io.Writer) {
				fmt.Fprintf(w, "NAME: %s\nREVISION: %d\n", rel.Name, rel.Revision)
				if len(results) == 0 {
					fmt.Fprintln(w, "TEST SUITE: None")
					return
				}
				for _, r := range results {
					phase := "Succeeded"
					if !r.Passed {
						phase = "Failed"
					}
					fmt.Fprintf(w, "TEST SUITE:     %s\nLast Started:   %s\nLast Completed: %s\nPhase:          %s\n",
						r.Name, formatTime(r.Started), formatTime(r.Finished), phase)
					if logs && r.Logs != "" {
						fmt.Fprintf(w, "LOGS:\n%s\n", strings.TrimRight(r.Logs, "\n"))
					}
				}
			})
			if runErr != nil {
				return runErr
			}
			return err
		},
	}
	cmd.Flags().DurationVar(&t.Timeout, "timeout", 5*time.Minute, "how long each test may run")
	cmd.Flags().StringSliceVar(&t.Filter, "filter", nil, "only run these tests (service names)")
	cmd.Flags().BoolVar(&logs, "logs", false, "print each test's output")
	format.register(cmd)
	return cmd
}
