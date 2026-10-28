package chart

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"composetpl/internal/glob"
)

// IgnoreFile lists paths to leave out of a chart, like Helm's .helmignore.
const IgnoreFile = ".composetplignore"

type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	// anchored rules contain a "/" and match the whole path; others match any basename.
	anchored bool
}

type ignoreRules []ignoreRule

// loadIgnore reads dir/.composetplignore. Syntax follows .gitignore: "#" comments,
// "!" negation, a trailing "/" for directories, a "/" anywhere else anchors the
// pattern to the chart root, and "*", "**" and "?" wildcards.
func loadIgnore(dir string) (ignoreRules, error) {
	data, err := os.ReadFile(filepath.Join(dir, IgnoreFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseIgnore(data)
}

func parseIgnore(data []byte) (ignoreRules, error) {
	var rules ignoreRules
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimSuffix(line, "/")
		}
		r.anchored = strings.Contains(line, "/")
		re, err := glob.Compile(strings.TrimPrefix(line, "/"))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", IgnoreFile, n, err)
		}
		r.re = re
		rules = append(rules, r)
	}
	return rules, sc.Err()
}

// ignored reports whether rel (a slash path) is excluded. Later rules win.
func (rs ignoreRules) ignored(rel string, isDir bool) bool {
	out := false
	for _, r := range rs {
		if r.dirOnly && !isDir {
			continue
		}
		subject := rel
		if !r.anchored {
			subject = path.Base(rel)
		}
		if r.re.MatchString(subject) {
			out = !r.negate
		}
	}
	return out
}
