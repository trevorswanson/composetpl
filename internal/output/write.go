package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevorswanson/composetpl/internal/engine"
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
		dst := filepath.Join(dir, filepath.FromSlash(splitPath(r.Name)))
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

// splitPath maps a template name to its output path: "<chart>/templates/a.yaml"
// becomes "a.yaml" and "<chart>/charts/<sub>/templates/a.yaml" becomes
// "charts/<sub>/a.yaml", so subcharts' files never collide with their parent's.
func splitPath(name string) string {
	parts := strings.Split(name, "/")
	var out []string
	i := 1
	for i+1 < len(parts) && parts[i] == "charts" {
		out = append(out, "charts", parts[i+1])
		i += 2
	}
	if i < len(parts) && parts[i] == "templates" {
		i++
	}
	return strings.Join(append(out, parts[i:]...), "/")
}
