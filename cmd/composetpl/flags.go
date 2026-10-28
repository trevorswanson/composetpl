package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"composetpl/internal/action"
	"composetpl/internal/compose"
	"composetpl/internal/output"
	"composetpl/internal/storage"
	"composetpl/internal/values"
)

// globalFlags select the Docker engine and release storage.
type globalFlags struct {
	context  string
	host     string
	storage  string
	stateDir string
}

func (g *globalFlags) register(root *cobra.Command) {
	fs := root.PersistentFlags()
	fs.StringVar(&g.context, "context", "", "docker context to use (like helm's --kube-context)")
	fs.StringVarP(&g.host, "host", "H", "", "docker daemon socket to connect to")
	fs.StringVar(&g.storage, "storage", envOr("COMPOSETPL_STORAGE", "volume"),
		"release storage driver: volume (on the Docker host) or local [$COMPOSETPL_STORAGE]")
	fs.StringVar(&g.stateDir, "state-dir", os.Getenv("COMPOSETPL_STATE_DIR"),
		"directory for the local storage driver (default $XDG_STATE_HOME/composetpl/<docker context>) [$COMPOSETPL_STATE_DIR]")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (g *globalFlags) dockerArgs() []string {
	var args []string
	if g.context != "" {
		args = append(args, "--context", g.context)
	}
	if g.host != "" {
		args = append(args, "--host", g.host)
	}
	return args
}

// contextName names the target engine, to keep local release state per engine.
func (g *globalFlags) contextName() string {
	switch {
	case g.context != "":
		return g.context
	case g.host != "":
		return sanitize(g.host)
	case os.Getenv("DOCKER_HOST") != "":
		return sanitize(os.Getenv("DOCKER_HOST"))
	case os.Getenv("DOCKER_CONTEXT") != "":
		return os.Getenv("DOCKER_CONTEXT")
	}
	if out, err := exec.Command("docker", "context", "show").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	return "default"
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	return strings.Trim(unsafeChars.ReplaceAllString(s, "_"), "_")
}

// testConfig, when set by tests, replaces the docker-backed configuration.
var testConfig func(cmd *cobra.Command) (*action.Configuration, error)

// config builds the action configuration. needCompose checks docker compose is
// installed and new enough; read-only commands on local storage skip it.
func (g *globalFlags) config(cmd *cobra.Command, needCompose bool) (*action.Configuration, error) {
	if testConfig != nil {
		return testConfig(cmd)
	}
	cli := compose.NewCLI(g.dockerArgs(), cmd.ErrOrStderr())
	if needCompose || g.storage == "volume" {
		if _, err := cli.Version(cmd.Context()); err != nil {
			return nil, err
		}
	}
	var store storage.Driver
	switch g.storage {
	case "volume":
		v := storage.NewVolume(g.dockerArgs(), os.Getenv("COMPOSETPL_HELPER_IMAGE"))
		v.Ctx = cmd.Context()
		store = v
	case "local":
		dir := g.stateDir
		if dir == "" {
			base := os.Getenv("XDG_STATE_HOME")
			if base == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return nil, err
				}
				base = filepath.Join(home, ".local", "state")
			}
			dir = filepath.Join(base, "composetpl", g.contextName())
		}
		store = storage.NewLocal(dir)
	default:
		return nil, fmt.Errorf("unknown --storage %q (want volume or local)", g.storage)
	}
	return &action.Configuration{Store: store, Compose: cli, Log: cmd.ErrOrStderr()}, nil
}

// valueFlags are the Helm value flags.
type valueFlags struct {
	values.Options
}

func (f *valueFlags) register(fs *pflag.FlagSet) {
	fs.StringArrayVarP(&f.ValueFiles, "values", "f", nil, "values file to merge over values.yaml (repeatable, later wins)")
	fs.StringArrayVar(&f.Values, "set", nil, "set a value, e.g. apps.plex.enabled=false (repeatable)")
	fs.StringArrayVar(&f.StringValues, "set-string", nil, "set a value, always as a string (repeatable)")
	fs.StringArrayVar(&f.FileValues, "set-file", nil, "set a value to a file's contents, e.g. key=path (repeatable)")
}

// renderFlags control template rendering.
type renderFlags struct {
	valueFlags
	strict bool
	noEnv  bool
	debug  bool
}

func (f *renderFlags) register(fs *pflag.FlagSet) {
	f.valueFlags.register(fs)
	fs.BoolVar(&f.strict, "strict", false, "fail on references to missing values")
	fs.BoolVar(&f.noEnv, "no-env", false, "disable the env and expandenv template functions")
	fs.BoolVar(&f.debug, "debug", false, "print the raw rendered output when it is not valid YAML")
}

// printDebug prints the raw output of a template that failed to parse, with --debug.
func (f *renderFlags) printDebug(w io.Writer, err error) {
	var pe *output.ParseError
	if !f.debug || !errors.As(err, &pe) {
		return
	}
	fmt.Fprintf(w, "--- raw output of %s ---\n", pe.Template)
	sc := bufio.NewScanner(strings.NewReader(pe.Content))
	for n := 1; sc.Scan(); n++ {
		fmt.Fprintf(w, "%4d | %s\n", n, sc.Text())
	}
	fmt.Fprintln(w, "---")
}

// deployFlags are shared by install, upgrade and rollback.
type deployFlags struct {
	action.DeployOptions
}

func (f *deployFlags) register(fs *pflag.FlagSet) {
	fs.BoolVar(&f.Wait, "wait", false, "wait until services are running and healthy (docker compose up --wait)")
	fs.DurationVar(&f.Timeout, "timeout", 5*time.Minute, "how long --wait waits")
	fs.StringVar(&f.Pull, "pull", "", "image pull policy: always, missing or never (default: compose's)")
	fs.BoolVar(&f.ForceRecreate, "force", false, "recreate containers even if their configuration is unchanged")
}

func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}
