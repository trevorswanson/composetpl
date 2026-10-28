//go:build integration

// Integration tests drive a real Docker engine. Run them with:
//
//	go test -tags integration -run Integration -v ./cmd/composetpl
//
// They need docker with the compose plugin (>= 2.20) and pull busybox,
// traefik/whoami and registry images. Everything they create is removed.
package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const integrationChart = `-- Chart.yaml --
name: whoami
version: 1.0.0
appVersion: "1.10"
-- values.yaml --
message: hello
-- templates/services.yaml --
services:
  whoami:
    image: traefik/whoami:v{{ .Chart.AppVersion }}
    command: ["--name", "{{ .Values.message }}"]
    healthcheck:
      test: ["CMD", "/whoami", "--help"]
      interval: 2s
      retries: 10
  smoke:
    image: busybox:1.37
    command: ["sh", "-c", "wget -qO- http://whoami | grep 'Name: {{ .Values.message }}'"]
    x-composetpl: {hook: test}
  marker:
    image: busybox:1.37
    command: ["true"]
    x-composetpl: {hook: [pre-upgrade, pre-rollback], delete-policy: hook-succeeded}
-- templates/NOTES.txt --
whoami says {{ .Values.message }}
`

func writeTxtar(t *testing.T, dir, archive string) {
	t.Helper()
	files := map[string]string{}
	var name string
	for _, line := range strings.SplitAfter(archive, "\n") {
		if strings.HasPrefix(line, "-- ") && strings.HasSuffix(strings.TrimSpace(line), " --") {
			name = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "-- "), " --")
			continue
		}
		files[name] += line
	}
	writeFiles(t, dir, files)
}

func requireCompose(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker engine is not reachable")
	}
}

func TestIntegrationLifecycle(t *testing.T) {
	requireCompose(t)
	chartDir := filepath.Join(t.TempDir(), "whoami")
	writeTxtar(t, chartDir, integrationChart)

	for _, storage := range []string{"local", "volume"} {
		t.Run(storage, func(t *testing.T) {
			name := fmt.Sprintf("ctpl-it-%s-%d", storage, rand.Intn(1e6))
			t.Setenv("COMPOSETPL_STORAGE", storage)
			t.Setenv("COMPOSETPL_STATE_DIR", t.TempDir())
			volume := "ctpl-it-releases-" + name
			t.Setenv("COMPOSETPL_VOLUME", volume)
			t.Cleanup(func() {
				_ = exec.Command("docker", "compose", "-p", name, "down", "--remove-orphans", "--volumes").Run()
				_ = exec.Command("docker", "volume", "rm", "-f", volume).Run()
			})

			expect(t, mustRun(t, "install", name, chartDir, "--wait", "--timeout", "2m"), "STATUS: deployed", "whoami says hello")
			expect(t, mustRun(t, "status", name), "whoami", "running")
			expect(t, mustRun(t, "test", name, "--logs"), "Phase:          Succeeded")

			expect(t, mustRun(t, "diff", "upgrade", name, chartDir, "--set", "message=bonjour"),
				"services/whoami, changed:", "+    - bonjour")
			mustRun(t, "upgrade", name, chartDir, "--set", "message=bonjour", "--wait")
			expect(t, mustRun(t, "test", name), "Succeeded")
			expect(t, mustRun(t, "list"), name, "running(1)")
			expect(t, mustRun(t, "history", name), "superseded", "deployed")

			expect(t, mustRun(t, "diff", "rollback", name), "-    - bonjour", "+    - hello")
			mustRun(t, "rollback", name, "--wait")
			expect(t, mustRun(t, "get", "values", name, "--all"), "message: hello")
			expect(t, mustRun(t, "test", name), "Succeeded")

			// An upgrade to an image that doesn't exist fails, and --atomic rolls it back.
			broken := filepath.Join(t.TempDir(), "whoami")
			writeTxtar(t, broken, strings.Replace(integrationChart, `appVersion: "1.10"`, `appVersion: "0.0.0-missing"`, 1))
			if _, err := run(t, "upgrade", name, broken, "--atomic", "--wait", "--timeout", "60s"); err == nil ||
				!strings.Contains(err.Error(), "rolled back") {
				t.Errorf("atomic upgrade: %v", err)
			}
			expect(t, mustRun(t, "status", name), "STATUS: deployed", "Rollback to")
			expect(t, mustRun(t, "test", name), "Succeeded")

			mustRun(t, "uninstall", name, "--keep-history")
			out, _ := exec.Command("docker", "compose", "ls", "-a", "--filter", "name="+name).CombinedOutput()
			if strings.Contains(string(out), name) {
				t.Errorf("project still exists after uninstall:\n%s", out)
			}
			expect(t, mustRun(t, "list", "--uninstalled"), name)
			mustRun(t, "uninstall", name)
		})
	}
}

func TestIntegrationRegistry(t *testing.T) {
	requireCompose(t)
	port := 5000 + rand.Intn(1000)
	container := fmt.Sprintf("ctpl-it-registry-%d", port)
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", container,
		"-p", fmt.Sprintf("127.0.0.1:%d:5000", port), "registry:2").CombinedOutput(); err != nil {
		t.Fatalf("starting registry: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	time.Sleep(2 * time.Second)

	chartDir := filepath.Join(t.TempDir(), "whoami")
	writeTxtar(t, chartDir, integrationChart)
	dist := t.TempDir()
	mustRun(t, "package", chartDir, "-d", dist)
	repo := fmt.Sprintf("oci://localhost:%d/charts", port)
	expect(t, mustRun(t, "push", filepath.Join(dist, "whoami-1.0.0.tgz"), repo), "Pushed:")
	expect(t, mustRun(t, "show", "chart", repo+"/whoami", "--version", "^1"), "version: 1.0.0")
	if got := mustRun(t, "template", repo+"/whoami:1.0.0"); got != mustRun(t, "template", chartDir) {
		t.Errorf("registry chart renders differently:\n%s", got)
	}
	_ = os.Remove(filepath.Join(dist, "whoami-1.0.0.tgz"))
}
