// Package release defines the record stored for every revision of a release.
package release

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// APIVersion versions the stored record format.
const APIVersion = "composetpl/v1"

// Status is the state of one revision, matching Helm's release statuses.
type Status string

const (
	StatusDeployed        Status = "deployed"
	StatusSuperseded      Status = "superseded"
	StatusFailed          Status = "failed"
	StatusUninstalled     Status = "uninstalled"
	StatusPendingInstall  Status = "pending-install"
	StatusPendingUpgrade  Status = "pending-upgrade"
	StatusPendingRollback Status = "pending-rollback"
)

// IsPending reports whether an operation is in progress on the revision.
func (s Status) IsPending() bool {
	return s == StatusPendingInstall || s == StatusPendingUpgrade || s == StatusPendingRollback
}

// ChartInfo identifies the chart a revision was rendered from.
type ChartInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	AppVersion  string `json:"appVersion,omitempty"`
	Description string `json:"description,omitempty"`
	// Ref is how the chart was given on the command line (path or oci:// reference).
	Ref string `json:"ref,omitempty"`
}

// String formats the chart as Helm does in `helm list`: name-version.
func (c ChartInfo) String() string {
	if c.Version == "" {
		return c.Name
	}
	return c.Name + "-" + c.Version
}

// Hook is a one-off service run at lifecycle events (see pkg/hooks).
type Hook struct {
	// Name is the compose service name.
	Name           string   `json:"name"`
	Events         []string `json:"events"`
	Weight         int      `json:"weight,omitempty"`
	DeletePolicies []string `json:"deletePolicies,omitempty"`
	NoDeps         bool     `json:"noDeps,omitempty"`
	// Retries is how many times a failed hook is run again.
	Retries int `json:"retries,omitempty"`
	// Backoff is the delay before the first retry (Go duration, doubling each time).
	Backoff string `json:"backoff,omitempty"`
	// Timeout bounds each attempt (Go duration), overriding the command's --timeout.
	Timeout string `json:"timeout,omitempty"`
	// Service is the service definition as YAML, without the x-composetpl key.
	Service string `json:"service"`
}

// Release is one revision of a release.
type Release struct {
	APIVersion  string    `json:"apiVersion"`
	Name        string    `json:"name"`
	Revision    int       `json:"revision"`
	Status      Status    `json:"status"`
	Description string    `json:"description,omitempty"`
	Chart       ChartInfo `json:"chart"`
	// Config holds the user-supplied values (-f/--set), as in `helm get values`.
	Config map[string]interface{} `json:"config,omitempty"`
	// Values holds the fully computed values used for rendering (`get values --all`).
	Values map[string]interface{} `json:"values,omitempty"`
	// Manifest is the merged compose file passed to `docker compose`, hooks excluded.
	Manifest string  `json:"manifest"`
	Hooks    []*Hook `json:"hooks,omitempty"`
	Notes    string  `json:"notes,omitempty"`
	// Labels are free-form metadata, like Helm's release labels. Tools that drive
	// composetpl use them to mark their releases (see LabelManagedBy).
	Labels map[string]string `json:"labels,omitempty"`
	// ProjectDir is the compose project directory relative paths resolve against.
	ProjectDir    string    `json:"projectDir,omitempty"`
	FirstDeployed time.Time `json:"firstDeployed"`
	LastDeployed  time.Time `json:"lastDeployed"`
}

// LabelManagedBy names the tool that manages a release, e.g. "composecd". The
// composetpl CLI warns before changing a release another tool manages.
const LabelManagedBy = "managed-by"

var labelKeyRegexp = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]{0,251}[A-Za-z0-9])?$`)

// ValidateLabels checks label keys: letters, digits, '.', '_', '-' and '/',
// starting and ending with a letter or digit, at most 253 characters.
func ValidateLabels(labels map[string]string) error {
	for k := range labels {
		if !labelKeyRegexp.MatchString(k) {
			return fmt.Errorf("invalid label key %q: must be letters, digits, '.', '_', '-' or '/', start and end with a letter or digit, and be at most 253 characters", k)
		}
	}
	return nil
}

// MergeLabels returns a new map of base overlaid by over. A key set to "" in over
// is removed, so callers can drop a label on upgrade.
func MergeLabels(base, over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Selector matches release labels.
type Selector func(labels map[string]string) bool

// ParseSelector parses a comma-separated label selector, as in `helm list -l`:
// "k=v" (or "k==v"), "k!=v" (also true when k is absent), "k" (present) and "!k"
// (absent). All must match.
func ParseSelector(s string) (Selector, error) {
	var tests []Selector
	for _, term := range strings.Split(s, ",") {
		term = strings.TrimSpace(term)
		var test Selector
		switch {
		case term == "":
			continue
		case strings.Contains(term, "!="):
			k, v, _ := strings.Cut(term, "!=")
			test = func(l map[string]string) bool { got, ok := l[k]; return !ok || got != v }
			term = k
		case strings.Contains(term, "="):
			k, v, _ := strings.Cut(term, "=")
			v = strings.TrimPrefix(v, "=")
			test = func(l map[string]string) bool { got, ok := l[k]; return ok && got == v }
			term = k
		case strings.HasPrefix(term, "!"):
			k := term[1:]
			test = func(l map[string]string) bool { _, ok := l[k]; return !ok }
			term = k
		default:
			k := term
			test = func(l map[string]string) bool { _, ok := l[k]; return ok }
		}
		if !labelKeyRegexp.MatchString(term) {
			return nil, fmt.Errorf("invalid label selector %q: bad key %q", s, term)
		}
		tests = append(tests, test)
	}
	return func(labels map[string]string) bool {
		for _, t := range tests {
			if !t(labels) {
				return false
			}
		}
		return true
	}, nil
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateName checks that name is usable as both a release and a compose project name.
func ValidateName(name string) error {
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("invalid release name %q: must be lowercase letters, digits, '-' or '_', start with a letter or digit, and be at most 63 characters", name)
	}
	return nil
}
