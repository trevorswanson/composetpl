// Package compose drives the `docker compose` CLI. Manifests are always passed on
// stdin (-f -) together with an explicit project name and directory, so nothing
// depends on compose files in the current directory.
package compose

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// MinVersion is the oldest compose release with every flag composetpl relies on
// (--wait-timeout, NDJSON ps output).
const MinVersion = "2.20.0"

// Project identifies a compose project and the manifest describing it.
type Project struct {
	Name     string
	Manifest []byte
	// Dir is the project directory relative paths resolve against.
	Dir string
}

// UpOptions maps to `docker compose up` flags.
type UpOptions struct {
	Wait          bool
	Timeout       time.Duration
	Pull          string // always|missing|never; empty = compose default
	ForceRecreate bool
}

// DownOptions maps to `docker compose down` flags.
type DownOptions struct {
	Volumes      bool
	RemoveImages string // local|all; empty = keep images
}

// RunOptions maps to a one-off `docker compose run`.
type RunOptions struct {
	Service       string
	ContainerName string
	NoDeps        bool
	// Output receives the container's stdout and stderr.
	Output io.Writer
}

// ConfigHashLabel is the container label where compose records the hash of the
// service configuration the container was created from.
const ConfigHashLabel = "com.docker.compose.config-hash"

// Container is one row of `docker compose ps`.
type Container struct {
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	Status   string `json:"Status"`
	ExitCode int    `json:"ExitCode"`
	Image    string `json:"Image"`
	Labels   Labels `json:"Labels"`
}

// ConfigHash is the hash of the service configuration the container was created
// from. Compare it with Runner.ConfigHash to detect drift.
func (c Container) ConfigHash() string { return c.Labels[ConfigHashLabel] }

// Labels are container labels. compose ps prints them as one "k=v,k=v" string;
// a JSON object is accepted too.
type Labels map[string]string

func (l *Labels) UnmarshalJSON(data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err == nil {
		*l = m
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("labels: want a string or an object: %w", err)
	}
	*l = parseLabels(s)
	return nil
}

// parseLabels splits compose's "k=v,k=v" label list. Values may themselves contain
// commas (Traefik rules often do), so a comma only starts a new label when the text
// after it looks like a label key followed by "=".
func parseLabels(s string) Labels {
	out := Labels{}
	if s == "" {
		return out
	}
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' && startsLabel(s[i+1:]) {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	for _, p := range parts {
		k, v, _ := strings.Cut(p, "=")
		out[k] = v
	}
	return out
}

// startsLabel reports whether s begins with a label key and "=". Keys are
// restricted to the characters Docker's reverse-DNS label convention uses.
func startsLabel(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '=':
			return i > 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_', c == '/':
		default:
			return false
		}
	}
	return false
}

// ProjectStatus is one row of `docker compose ls`.
type ProjectStatus struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

// Runner is everything composetpl needs from docker compose. The CLI type
// implements it; tests use fakes.
type Runner interface {
	Version(ctx context.Context) (string, error)
	Config(ctx context.Context, p Project) error
	// ConfigHash returns each service's config hash (`docker compose config --hash`),
	// which compose also stores on the containers it creates (Container.ConfigHash).
	ConfigHash(ctx context.Context, p Project) (map[string]string, error)
	Up(ctx context.Context, p Project, o UpOptions) error
	Down(ctx context.Context, p Project, o DownOptions) error
	Run(ctx context.Context, p Project, o RunOptions) error
	RemoveContainer(ctx context.Context, name string) error
	PS(ctx context.Context, project string) ([]Container, error)
	Ls(ctx context.Context) ([]ProjectStatus, error)
	// ServerVersion is the Docker engine version.
	ServerVersion(ctx context.Context) (string, error)
	// Inspect returns `docker <kind> inspect <name>`, or an empty map if it does not exist.
	Inspect(ctx context.Context, kind, name string) (map[string]interface{}, error)
}

// LookupKinds are the object kinds Inspect (and the lookup template function) supports.
var LookupKinds = map[string]bool{"container": true, "image": true, "network": true, "volume": true}

// ExecFunc runs the docker binary with args. It exists so tests can capture invocations.
type ExecFunc func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// CLI runs the real docker binary.
type CLI struct {
	// Global are docker-level flags placed before "compose", e.g. --context X or -H host.
	Global []string
	// Env is added to the process environment of every docker call, as KEY=VALUE
	// entries that win over the process's own. It feeds compose's ${VAR}
	// interpolation and settings such as DOCKER_HOST, without touching os.Environ,
	// so CLIs with different Env can run concurrently.
	Env []string
	// Progress receives compose's progress output for up/down/run.
	Progress io.Writer
	// Exec runs docker; nil uses DockerExec(Env).
	Exec ExecFunc
}

// NewCLI returns a CLI using the docker binary on PATH.
func NewCLI(global []string, progress io.Writer) *CLI {
	return &CLI{Global: global, Progress: progress}
}

// DockerExec returns an ExecFunc that runs the docker binary on PATH with the
// process environment plus env (later entries win).
func DockerExec(env []string) ExecFunc {
	return func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		docker, err := exec.LookPath("docker")
		if err != nil {
			return fmt.Errorf("docker not found on PATH: %w", err)
		}
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		cmd.Env = append(os.Environ(), env...)
		return cmd.Run()
	}
}

func (c *CLI) exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if c.Exec != nil {
		return c.Exec(ctx, args, stdin, stdout, stderr)
	}
	return DockerExec(c.Env)(ctx, args, stdin, stdout, stderr)
}

func (c *CLI) run(ctx context.Context, args []string, stdin []byte, stdout io.Writer) error {
	full := append(append(append([]string{}, c.Global...), "compose"), args...)
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	errOut := io.Writer(&stderr)
	if c.Progress != nil {
		errOut = io.MultiWriter(&stderr, c.Progress)
	}
	if err := c.exec(ctx, full, in, stdout, errOut); err != nil {
		if msg := lastLines(stderr.String(), 5); msg != "" {
			return fmt.Errorf("docker compose %s: %w: %s", args[0], err, msg)
		}
		return fmt.Errorf("docker compose %s: %w", args[0], err)
	}
	return nil
}

// projectArgs selects the project and reads the manifest from stdin.
func projectArgs(p Project) []string {
	args := []string{"-p", p.Name, "-f", "-"}
	if p.Dir != "" {
		args = append(args, "--project-directory", p.Dir)
	}
	return args
}

// Version returns compose's version and errors if it is older than MinVersion.
func (c *CLI) Version(ctx context.Context) (string, error) {
	var out bytes.Buffer
	full := append(append([]string{}, c.Global...), "compose", "version", "--short")
	if err := c.exec(ctx, full, nil, &out, io.Discard); err != nil {
		return "", fmt.Errorf("docker compose is not available; install Docker Compose v2 (%s or newer): %w", MinVersion, err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(out.String()), "v")
	sv, err := semver.NewVersion(v)
	if err != nil {
		return v, fmt.Errorf("unrecognised docker compose version %q", v)
	}
	if sv.LessThan(semver.MustParse(MinVersion)) {
		return v, fmt.Errorf("docker compose %s is too old; composetpl needs %s or newer", v, MinVersion)
	}
	return v, nil
}

func (c *CLI) Config(ctx context.Context, p Project) error {
	return c.run(ctx, append(projectArgs(p), "config", "--quiet"), p.Manifest, io.Discard)
}

func (c *CLI) ConfigHash(ctx context.Context, p Project) (map[string]string, error) {
	var out bytes.Buffer
	if err := c.run(ctx, append(projectArgs(p), "config", "--hash", "*"), p.Manifest, &out); err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		if service, hash, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			hashes[service] = hash
		}
	}
	return hashes, nil
}

func (c *CLI) Up(ctx context.Context, p Project, o UpOptions) error {
	args := append(projectArgs(p), "up", "--detach", "--remove-orphans")
	if o.Wait {
		args = append(args, "--wait")
		if o.Timeout > 0 {
			args = append(args, "--wait-timeout", strconv.Itoa(int(o.Timeout.Seconds())))
		}
	}
	if o.Pull != "" {
		args = append(args, "--pull", o.Pull)
	}
	if o.ForceRecreate {
		args = append(args, "--force-recreate")
	}
	return c.run(ctx, args, p.Manifest, c.Progress)
}

func (c *CLI) Down(ctx context.Context, p Project, o DownOptions) error {
	args := append(projectArgs(p), "down", "--remove-orphans")
	if o.Volumes {
		args = append(args, "--volumes")
	}
	if o.RemoveImages != "" {
		args = append(args, "--rmi", o.RemoveImages)
	}
	return c.run(ctx, args, p.Manifest, c.Progress)
}

func (c *CLI) Run(ctx context.Context, p Project, o RunOptions) error {
	args := append(projectArgs(p), "run", "--no-TTY", "--interactive=false")
	if o.ContainerName != "" {
		args = append(args, "--name", o.ContainerName)
	}
	if o.NoDeps {
		args = append(args, "--no-deps")
	}
	args = append(args, o.Service)
	out := o.Output
	if out == nil {
		out = c.Progress
	}
	return c.run(ctx, args, p.Manifest, out)
}

// RemoveContainer force-removes a container, ignoring "no such container".
func (c *CLI) RemoveContainer(ctx context.Context, name string) error {
	var stderr bytes.Buffer
	err := c.exec(ctx, append(append([]string{}, c.Global...), "rm", "--force", name), nil, io.Discard, &stderr)
	if err != nil && !strings.Contains(strings.ToLower(stderr.String()), "no such container") {
		return fmt.Errorf("docker rm %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (c *CLI) PS(ctx context.Context, project string) ([]Container, error) {
	var out bytes.Buffer
	if err := c.run(ctx, []string{"-p", project, "ps", "--all", "--format", "json"}, nil, &out); err != nil {
		return nil, err
	}
	return ParsePS(out.Bytes())
}

func (c *CLI) Ls(ctx context.Context) ([]ProjectStatus, error) {
	var out bytes.Buffer
	if err := c.run(ctx, []string{"ls", "--all", "--format", "json"}, nil, &out); err != nil {
		return nil, err
	}
	var rows []ProjectStatus
	if len(bytes.TrimSpace(out.Bytes())) == 0 {
		return rows, nil
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		return nil, fmt.Errorf("parsing docker compose ls output: %w", err)
	}
	return rows, nil
}

func (c *CLI) ServerVersion(ctx context.Context) (string, error) {
	var out, stderr bytes.Buffer
	args := append(append([]string{}, c.Global...), "version", "--format", "{{.Server.Version}}")
	if err := c.exec(ctx, args, nil, &out, &stderr); err != nil {
		return "", fmt.Errorf("docker version: %w: %s", err, lastLines(stderr.String(), 3))
	}
	return strings.TrimSpace(out.String()), nil
}

func (c *CLI) Inspect(ctx context.Context, kind, name string) (map[string]interface{}, error) {
	if !LookupKinds[kind] {
		return nil, fmt.Errorf("lookup: unsupported kind %q (want container, image, network or volume)", kind)
	}
	var out, stderr bytes.Buffer
	args := append(append([]string{}, c.Global...), kind, "inspect", "--", name)
	if err := c.exec(ctx, args, nil, &out, &stderr); err != nil {
		msg := strings.ToLower(stderr.String())
		if strings.Contains(msg, "no such") || strings.Contains(msg, "not found") {
			return map[string]interface{}{}, nil
		}
		return nil, fmt.Errorf("docker %s inspect %s: %w: %s", kind, name, err, lastLines(stderr.String(), 3))
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		return nil, fmt.Errorf("parsing docker %s inspect output: %w", kind, err)
	}
	if len(rows) == 0 {
		return map[string]interface{}{}, nil
	}
	return rows[0], nil
}

// ParsePS accepts both output formats of `docker compose ps --format json`:
// a JSON array (compose < 2.21) or one JSON object per line.
func ParsePS(data []byte) ([]Container, error) {
	data = bytes.TrimSpace(data)
	var rows []Container
	if len(data) == 0 {
		return rows, nil
	}
	if data[0] == '[' {
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("parsing docker compose ps output: %w", err)
		}
		return rows, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c Container
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("parsing docker compose ps output: %w", err)
		}
		rows = append(rows, c)
	}
	return rows, sc.Err()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
