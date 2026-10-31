//go:build integration

package storage

import (
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"testing"

	"github.com/trevorswanson/composetpl/pkg/release"
)

// TestIntegrationVolume round-trips records through a real engine's volume, and
// checks that the accessor is never started and is recreated when removed.
func TestIntegrationVolume(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker engine is not reachable")
	}
	volume := fmt.Sprintf("ctpl-it-storage-%d", rand.Intn(1e6))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", AccessorName(volume)).Run()
		_ = exec.Command("docker", "volume", "rm", "-f", volume).Run()
	})
	newVolume := func() *Volume {
		v := NewVolume(nil, "")
		v.VolumeName = volume
		return v
	}

	if err := newVolume().Save(rel("media", 1, release.StatusSuperseded), rel("media", 2, release.StatusDeployed)); err != nil {
		t.Fatal(err)
	}
	h, err := newVolume().History("media")
	if err != nil || len(h) != 2 || h[1].Config["secret"] != "s3cret" {
		t.Fatalf("History = %v, %v", h, err)
	}
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", AccessorName(volume)).Output()
	if err != nil || strings.TrimSpace(string(out)) != "created" {
		t.Errorf("accessor state = %q, %v; want created (never started)", out, err)
	}

	// After `docker container prune`, the next call recreates the accessor.
	if err := exec.Command("docker", "rm", AccessorName(volume)).Run(); err != nil {
		t.Fatal(err)
	}
	v := newVolume()
	v.ensured = true // as if this process had created it before the prune
	if err := v.Delete(h[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("media", 1); err == nil {
		t.Error("revision 1 survived Delete")
	}
	if _, err := newVolume().Get("media", 2); err != nil {
		t.Errorf("after recreating the accessor: %v", err)
	}
}
