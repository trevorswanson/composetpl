package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"composetpl/internal/release"
)

// Local stores records as JSON files under Root/<release>/v<rev>.json.
// Files are 0600 because records contain user values, which may hold secrets.
type Local struct {
	Root string
}

// NewLocal returns a Local driver rooted at dir.
func NewLocal(dir string) *Local { return &Local{Root: dir} }

func (l *Local) Name() string { return "local" }

func (l *Local) load(name string) (records, error) {
	rs := records{}
	root := l.Root
	if name != "" {
		root = filepath.Join(l.Root, name)
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(l.Root, p)
		if _, _, ok := parseRecordPath(filepath.ToSlash(rel)); !ok {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, err := decode(data)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rs.put(r)
		return nil
	})
	return rs, err
}

func (l *Local) Get(name string, revision int) (*release.Release, error) {
	rs, err := l.load(name)
	if err != nil {
		return nil, err
	}
	return rs.get(name, revision)
}

func (l *Local) History(name string) ([]*release.Release, error) {
	rs, err := l.load(name)
	if err != nil {
		return nil, err
	}
	return rs.history(name)
}

func (l *Local) List() ([]*release.Release, error) {
	rs, err := l.load("")
	if err != nil {
		return nil, err
	}
	return rs.list(), nil
}

func (l *Local) Save(rels ...*release.Release) error {
	for _, r := range rels {
		data, err := encode(r)
		if err != nil {
			return err
		}
		p := filepath.Join(l.Root, filepath.FromSlash(recordPath(r.Name, r.Revision)))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	return nil
}

func (l *Local) Delete(rels ...*release.Release) error {
	for _, r := range rels {
		p := filepath.Join(l.Root, filepath.FromSlash(recordPath(r.Name, r.Revision)))
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		// Remove the release directory once it is empty.
		_ = os.Remove(filepath.Dir(p))
	}
	return nil
}
