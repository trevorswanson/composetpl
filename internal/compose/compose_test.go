package compose

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	args   []string
	stdin  string
	stdout string
	err    error
}

func (r *recorder) exec(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	r.args = args
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		r.stdin = string(b)
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, r.stdout)
	}
	return r.err
}

func TestCommandLines(t *testing.T) {
	p := Project{Name: "media", Manifest: []byte("services: {}\n"), Dir: "/charts/media"}
	ctx := context.Background()
	cases := []struct {
		name string
		call func(c *CLI) error
		want string
	}{
		{"up", func(c *CLI) error {
			return c.Up(ctx, p, UpOptions{Wait: true, Timeout: 90 * time.Second, Pull: "always", ForceRecreate: true})
		}, "--context nas compose -p media -f - --project-directory /charts/media up --detach --remove-orphans --wait --wait-timeout 90 --pull always --force-recreate"},
		{"down", func(c *CLI) error { return c.Down(ctx, p, DownOptions{Volumes: true, RemoveImages: "local"}) },
			"--context nas compose -p media -f - --project-directory /charts/media down --remove-orphans --volumes --rmi local"},
		{"run", func(c *CLI) error {
			return c.Run(ctx, p, RunOptions{Service: "migrate", ContainerName: "media-hook-migrate", NoDeps: true})
		}, "--context nas compose -p media -f - --project-directory /charts/media run --no-TTY --interactive=false --name media-hook-migrate --no-deps migrate"},
		{"config", func(c *CLI) error { return c.Config(ctx, p) },
			"--context nas compose -p media -f - --project-directory /charts/media config --quiet"},
		{"rm", func(c *CLI) error { return c.RemoveContainer(ctx, "media-hook-migrate") },
			"--context nas rm --force media-hook-migrate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			c := &CLI{Global: []string{"--context", "nas"}, Exec: r.exec}
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(r.args, " "); got != tc.want {
				t.Errorf("args:\n got %s\nwant %s", got, tc.want)
			}
			if tc.name != "rm" && r.stdin != "services: {}\n" {
				t.Errorf("manifest not passed on stdin: %q", r.stdin)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	for out, wantErr := range map[string]string{
		"v2.29.1\n": "",
		"2.20.0":    "",
		"2.17.3":    "too old",
		"garbage":   "unrecognised",
	} {
		c := &CLI{Exec: (&recorder{stdout: out}).exec}
		_, err := c.Version(context.Background())
		if (wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), wantErr)) {
			t.Errorf("%q: got %v, want %q", out, err, wantErr)
		}
	}
	c := &CLI{Exec: (&recorder{err: errors.New("exit status 1")}).exec}
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("missing plugin: %v", err)
	}
}

func TestParsePS(t *testing.T) {
	ndjson := `{"Name":"media-web-1","Service":"web","State":"running","Health":"healthy"}
{"Name":"media-db-1","Service":"db","State":"exited","ExitCode":1}
`
	array := `[{"Name":"media-web-1","Service":"web","State":"running","Health":"healthy"},{"Name":"media-db-1","Service":"db","State":"exited","ExitCode":1}]`
	for _, in := range []string{ndjson, array} {
		rows, err := ParsePS([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].Health != "healthy" || rows[1].ExitCode != 1 {
			t.Errorf("rows = %+v", rows)
		}
	}
	if rows, err := ParsePS(nil); err != nil || len(rows) != 0 {
		t.Errorf("empty: %v %v", rows, err)
	}
}

func TestErrorIncludesStderr(t *testing.T) {
	c := &CLI{Exec: func(_ context.Context, _ []string, _ io.Reader, _, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "pulling\nservice \"web\" is unhealthy\n")
		return errors.New("exit status 1")
	}}
	err := c.Up(context.Background(), Project{Name: "x"}, UpOptions{})
	if err == nil || !strings.Contains(err.Error(), `service "web" is unhealthy`) {
		t.Errorf("got %v", err)
	}
}
