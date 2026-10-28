package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"composetpl/internal/engine"
)

// WriteSplit writes each rendered template to dir, keeping its path relative to
// templates/. Templates that render to only whitespace are skipped. Every file is
// checked to be valid YAML first. It returns the paths written.
func WriteSplit(dir string, rendered []engine.Rendered) ([]string, error) {
	var written []string
	for _, r := range rendered {
		if strings.TrimSpace(r.Content) == "" {
			continue
		}
		if _, err := ParseDocs(r); err != nil {
			return written, err
		}
		rel := r.Name
		if i := strings.Index(rel, "/templates/"); i >= 0 {
			rel = rel[i+len("/templates/"):]
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return written, err
		}
		content := strings.TrimLeft(r.Content, "\n")
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return written, fmt.Errorf("writing %s: %w", dst, err)
		}
		written = append(written, dst)
	}
	return written, nil
}
