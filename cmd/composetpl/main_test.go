package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const exampleChart = "../../examples/homelab/stack"

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./cmd/composetpl -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run with -update if intended)\n--- got ---\n%s", name, got)
	}
}

func TestTemplateMergedGolden(t *testing.T) {
	out, err := run(t, "template", exampleChart)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "homelab-merged.yaml", out)
}

func TestTemplateSetOverrides(t *testing.T) {
	out, err := run(t, "template", exampleChart, "--name", "prod",
		"--set", "apps.plex.enabled=false", "--set", "apps.jellyfin.enabled=true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "name: prod\n") {
		t.Errorf("expected project name prod, got:\n%s", out)
	}
	if strings.Contains(out, "  plex:") || !strings.Contains(out, "  jellyfin:") {
		t.Errorf("--set did not toggle apps:\n%s", out)
	}
}

func TestTemplateSplitGolden(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, "template", exampleChart, "--output-dir", dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"apps.yaml", "networks.yaml", "traefik.yaml"} {
		got, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, filepath.Join("homelab-split", f), string(got))
	}
}

func TestLint(t *testing.T) {
	out, err := run(t, "lint", exampleChart)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "lint passed") {
		t.Errorf("unexpected output:\n%s", out)
	}

	out, err = run(t, "lint", exampleChart, "--set", "apps.sonarr.image=null")
	if err == nil || !strings.Contains(err.Error(), "apps.sonarr.image is required") {
		t.Errorf("expected required error, got %v\n%s", err, out)
	}
}

func TestOutputFlagsExclusive(t *testing.T) {
	_, err := run(t, "template", exampleChart, "-o", "x", "--output-dir", "y")
	if err == nil {
		t.Fatal("expected an error")
	}
}
