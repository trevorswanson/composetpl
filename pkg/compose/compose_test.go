package compose

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
		{"hash", func(c *CLI) error { _, err := c.ConfigHash(ctx, p); return err },
			"--context nas compose -p media -f - --project-directory /charts/media config --hash *"},
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

func TestConfigHash(t *testing.T) {
	c := &CLI{Exec: (&recorder{stdout: "db 1111\nweb abcd\n\n"}).exec}
	got, err := c.ConfigHash(context.Background(), Project{Name: "media"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["db"] != "1111" || got["web"] != "abcd" {
		t.Errorf("hashes = %v", got)
	}
}

func TestContainerLabels(t *testing.T) {
	rule := "traefik.http.routers.web.rule=Host(`a.lan`) || Host(`b.lan`),Path(`/x`)"
	in := `{"Name":"media-web-1","Service":"web","Labels":"com.docker.compose.config-hash=abcd,` + rule +
		`,com.docker.compose.project=media,empty="}
{"Name":"media-db-1","Service":"db","Labels":{"com.docker.compose.config-hash":"1111"}}
{"Name":"media-old-1","Service":"old"}
`
	rows, err := ParsePS([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	web := rows[0].Labels
	if rows[0].ConfigHash() != "abcd" || web["com.docker.compose.project"] != "media" || len(web) != 4 {
		t.Errorf("web labels = %v", web)
	}
	if got := web["traefik.http.routers.web.rule"]; got != "Host(`a.lan`) || Host(`b.lan`),Path(`/x`)" {
		t.Errorf("value with commas = %q", got)
	}
	if v, ok := web["empty"]; !ok || v != "" {
		t.Errorf("empty label = %q, %v", v, ok)
	}
	if rows[1].ConfigHash() != "1111" || rows[2].ConfigHash() != "" {
		t.Errorf("hashes = %q %q", rows[1].ConfigHash(), rows[2].ConfigHash())
	}
}

func TestDockerExecEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as a fake docker")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$COMPOSETPL_TEST_A $COMPOSETPL_TEST_B\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMPOSETPL_TEST_A", "process")
	t.Setenv("COMPOSETPL_TEST_B", "process")

	// Two CLIs with different Env run concurrently without seeing each other's values.
	var wg sync.WaitGroup
	got := make([]string, 2)
	for i, v := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &CLI{Env: []string{"COMPOSETPL_TEST_B=" + v}}
			var out bytes.Buffer
			if err := c.exec(context.Background(), nil, nil, &out, io.Discard); err != nil {
				t.Error(err)
			}
			got[i] = strings.TrimSpace(out.String())
		}()
	}
	wg.Wait()
	if got[0] != "process one" || got[1] != "process two" {
		t.Errorf("got %q", got)
	}
	if os.Getenv("COMPOSETPL_TEST_B") != "process" {
		t.Error("Env leaked into the process environment")
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
