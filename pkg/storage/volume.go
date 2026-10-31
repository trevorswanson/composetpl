package storage

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/trevorswanson/composetpl/pkg/compose"
	"github.com/trevorswanson/composetpl/pkg/release"
)

const (
	// DefaultVolume is the Docker volume that holds release records.
	DefaultVolume = "composetpl-releases"
	// DefaultHelperImage is the image of the accessor container and of the
	// helper that deletes records.
	DefaultHelperImage = "busybox:1.37"
	// Label marks the volume and its accessor container, with value LabelValue.
	Label       = "io.composetpl.storage"
	LabelValue  = "v1"
	volumeLabel = Label + "=" + LabelValue
)

// AccessorName is the accessor container of volume.
func AccessorName(volume string) string { return volume + "-accessor" }

// Volume stores records in a Docker volume on the target engine, so release history
// travels with the host and is shared by every client that talks to it.
//
// The Docker API can't read or write a volume's files directly, only a
// container's (docker cp), and it can do that for a container that was created
// but never started. So the driver keeps one such accessor container per volume
// and copies records in and out of it: reads and writes start no container.
// docker cp can't delete files, so Delete still runs a short-lived helper with
// no network. Records are cached for the process.
type Volume struct {
	// Global are docker-level flags, e.g. --context X.
	Global     []string
	VolumeName string
	// Accessor is the accessor container's name; empty means AccessorName(VolumeName).
	Accessor    string
	HelperImage string
	// Env is added to the environment of every docker call (see compose.CLI.Env).
	Env []string
	// Exec runs docker; nil uses compose.DockerExec(Env).
	Exec compose.ExecFunc
	Ctx  context.Context

	cache   records
	ensured bool
}

// NewVolume returns a Volume driver using the docker binary on PATH.
func NewVolume(global []string, helperImage string) *Volume {
	if helperImage == "" {
		helperImage = DefaultHelperImage
	}
	return &Volume{
		Global:      global,
		VolumeName:  DefaultVolume,
		HelperImage: helperImage,
		Ctx:         context.Background(),
	}
}

func (v *Volume) Name() string { return "volume" }

func (v *Volume) accessor() string {
	if v.Accessor != "" {
		return v.Accessor
	}
	return AccessorName(v.VolumeName)
}

func (v *Volume) docker(stdin io.Reader, stdout io.Writer, args ...string) error {
	var stderr bytes.Buffer
	full := append(append([]string{}, v.Global...), args...)
	ctx, cancel := context.WithTimeout(v.Ctx, 2*time.Minute)
	defer cancel()
	run := v.Exec
	if run == nil {
		run = compose.DockerExec(v.Env)
	}
	if err := run(ctx, full, stdin, stdout, &stderr); err != nil {
		return fmt.Errorf("volume storage: docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// helper is the docker run arguments of a short-lived helper running cmd
// against the volume.
func (v *Volume) helper(cmd ...string) []string {
	args := []string{"run", "--rm", "--network", "none", "--pull", "missing", "-v", v.VolumeName + ":/data"}
	return append(append(args, v.HelperImage), cmd...)
}

func (v *Volume) ensure() error {
	if v.ensured {
		return nil
	}
	if err := v.docker(nil, io.Discard, "volume", "create", "--label", volumeLabel, v.VolumeName); err != nil {
		return err
	}
	// The accessor only exists to be copied to and from; its command never runs.
	err := v.docker(nil, io.Discard, "create", "--name", v.accessor(), "--label", volumeLabel,
		"--network", "none", "--pull", "missing", "-v", v.VolumeName+":/data", v.HelperImage, "true")
	// A conflict means the accessor exists, made earlier or by another client.
	if err != nil && !strings.Contains(err.Error(), "is already in use") {
		return err
	}
	v.ensured = true
	return nil
}

// copy pipes a tar of the volume to stdout, or with in set, extracts the tar
// in into the volume. It recreates the accessor once if it's gone (for
// example after `docker container prune`).
func (v *Volume) copy(in []byte, stdout io.Writer) error {
	run := func() error {
		if in == nil {
			return v.docker(nil, stdout, "cp", v.accessor()+":/data/.", "-")
		}
		return v.docker(bytes.NewReader(in), stdout, "cp", "-", v.accessor()+":/data")
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "No such container") {
		return err
	}
	v.ensured = false
	if err := v.ensure(); err != nil {
		return err
	}
	return run()
}

func (v *Volume) load() (records, error) {
	if v.cache != nil {
		return v.cache, nil
	}
	if err := v.ensure(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := v.copy(nil, &out); err != nil {
		return nil, err
	}
	rs := records{}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("volume storage: reading archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if _, _, ok := parseRecordPath(h.Name); !ok {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		r, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("volume storage: %s: %w", h.Name, err)
		}
		rs.put(r)
	}
	v.cache = rs
	return rs, nil
}

func (v *Volume) Get(name string, revision int) (*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.get(name, revision)
}

func (v *Volume) History(name string) ([]*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.history(name)
}

func (v *Volume) List() ([]*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.list(), nil
}

func (v *Volume) Save(rels ...*release.Release) error {
	rs, err := v.load()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, r := range rels {
		data, err := encode(r)
		if err != nil {
			return err
		}
		if !dirs[r.Name] {
			dirs[r.Name] = true
			if err := tw.WriteHeader(&tar.Header{Name: r.Name + "/", Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Now()}); err != nil {
				return err
			}
		}
		h := &tar.Header{Name: recordPath(r.Name, r.Revision), Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := v.copy(buf.Bytes(), io.Discard); err != nil {
		return err
	}
	for _, r := range rels {
		rs.put(r)
	}
	return nil
}

func (v *Volume) Delete(rels ...*release.Release) error {
	if len(rels) == 0 {
		return nil
	}
	rs, err := v.load()
	if err != nil {
		return err
	}
	var files []string
	dirs := map[string]bool{}
	for _, r := range rels {
		// Names are validated, so they are safe to place in a shell command.
		if err := release.ValidateName(r.Name); err != nil {
			return err
		}
		files = append(files, path.Join("/data", recordPath(r.Name, r.Revision)))
		dirs[path.Join("/data", r.Name)] = true
	}
	script := "rm -f " + strings.Join(files, " ")
	for d := range dirs {
		script += "; rmdir " + d + " 2>/dev/null"
	}
	script += "; true"
	// docker cp can't delete, so this is the one operation that starts a container.
	if err := v.docker(nil, io.Discard, v.helper("sh", "-c", script)...); err != nil {
		return err
	}
	for _, r := range rels {
		rs.remove(r)
	}
	return nil
}
