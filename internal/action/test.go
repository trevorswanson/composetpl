package action

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/trevorswanson/composetpl/internal/hooks"
	"github.com/trevorswanson/composetpl/internal/release"
	"github.com/trevorswanson/composetpl/internal/storage"
)

// TestResult is the outcome of one test hook.
type TestResult struct {
	Name     string    `json:"name"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Passed   bool      `json:"passed"`
	Attempts int       `json:"attempts"`
	Error    string    `json:"error,omitempty"`
	// Logs holds the test container's output.
	Logs string `json:"logs,omitempty"`
}

// ReleaseTesting runs a release's test hooks, like `helm test`.
type ReleaseTesting struct {
	cfg     *Configuration
	Timeout time.Duration
	// Filter limits the run to these test names.
	Filter []string
}

// NewReleaseTesting returns a ReleaseTesting action.
func NewReleaseTesting(cfg *Configuration) *ReleaseTesting {
	return &ReleaseTesting{cfg: cfg, Timeout: 5 * time.Minute}
}

// Run runs every test hook of the deployed revision in order and stops at the
// first failure, returning one result per test that ran.
func (t *ReleaseTesting) Run(ctx context.Context, name string) (*release.Release, []TestResult, error) {
	rel, err := storage.LastDeployed(t.cfg.Store, name)
	if err != nil {
		return nil, nil, err
	}
	tests := hooks.For(rel, hooks.Test)
	if len(tests) == 0 {
		return rel, nil, nil
	}
	var results []TestResult
	for _, h := range tests {
		if len(t.Filter) > 0 && !contains(t.Filter, h.Name) {
			continue
		}
		var logs bytes.Buffer
		r := &hooks.Runner{Compose: t.cfg.Compose, Output: &logs, Log: io.Discard, Timeout: t.Timeout, Sleep: t.cfg.HookSleep}
		res, err := r.Run(ctx, rel, hooks.Test, h.Name)
		tr := TestResult{Name: h.Name, Passed: err == nil, Logs: logs.String()}
		if len(res) > 0 {
			tr.Started, tr.Finished, tr.Attempts = res[0].Started, res[0].Finished, res[0].Attempts
		}
		if err != nil {
			tr.Error = err.Error()
		}
		results = append(results, tr)
		if err != nil {
			return rel, results, fmt.Errorf("test %q failed", h.Name)
		}
	}
	return rel, results, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
