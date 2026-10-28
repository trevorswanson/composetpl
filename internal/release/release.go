// Package release defines the record stored for every revision of a release.
package release

import (
	"fmt"
	"regexp"
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

// Hook is a one-off service run at lifecycle events (see internal/hooks).
type Hook struct {
	// Name is the compose service name.
	Name           string   `json:"name"`
	Events         []string `json:"events"`
	Weight         int      `json:"weight,omitempty"`
	DeletePolicies []string `json:"deletePolicies,omitempty"`
	NoDeps         bool     `json:"noDeps,omitempty"`
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
	// ProjectDir is the compose project directory relative paths resolve against.
	ProjectDir    string    `json:"projectDir,omitempty"`
	FirstDeployed time.Time `json:"firstDeployed"`
	LastDeployed  time.Time `json:"lastDeployed"`
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateName checks that name is usable as both a release and a compose project name.
func ValidateName(name string) error {
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("invalid release name %q: must be lowercase letters, digits, '-' or '_', start with a letter or digit, and be at most 63 characters", name)
	}
	return nil
}
