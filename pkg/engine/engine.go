/*
Portions adapted from Helm (helm.sh/helm/v3/pkg/engine/engine.go)
and modified for composetpl.
Copyright The Helm Authors. Licensed under the Apache License, Version 2.0.
*/

// Package engine renders a chart's templates with Go text/template, Sprig and
// Helm's built-in functions (include, tpl, required, toYaml, ...).
package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/trevorswanson/composetpl/pkg/chart"
)

// Options controls rendering behavior.
type Options struct {
	// Strict fails on references to missing map keys instead of rendering them empty.
	Strict bool
	// NoEnv removes the env and expandenv functions, as Helm does.
	NoEnv bool
	// Env backs the env and expandenv functions. When nil, they read the process
	// environment, as Sprig's do.
	Env func(key string) (string, bool)
	// Capabilities populates .Capabilities.
	Capabilities map[string]interface{}
	// Lookup implements the lookup function. When nil, lookup returns an empty
	// map, as Helm's does without a cluster (template, lint, --dry-run).
	Lookup func(kind, name string) (map[string]interface{}, error)
	// SubchartNotes also renders subcharts' NOTES.txt (helm --render-subchart-notes).
	SubchartNotes bool
}

// Rendered is the output of one non-partial template.
type Rendered struct {
	Name    string
	Content string
}

// Result is the output of rendering a chart.
type Result struct {
	Templates []Rendered
	// Notes is the rendered templates/NOTES.txt of the root chart, followed by
	// the subcharts' when Options.SubchartNotes is set.
	Notes string
}

const recursionMaxNums = 1000

const (
	warnStartDelim = "COMPOSETPL_ERR_START"
	warnEndDelim   = "COMPOSETPL_ERR_END"
)

var warnRegex = regexp.MustCompile(warnStartDelim + `((?s).*)` + warnEndDelim)

func warnWrap(warn string) string {
	return warnStartDelim + warn + warnEndDelim
}

// Render parses every template of c and its dependencies into one set, then renders
// the non-partial templates and NOTES.txt of c and, recursively, of its application
// (non-library) dependencies. Each chart sees its own .Chart, .Files and scoped
// .Values (vals.<name> for a subchart, as prepared by chart.ProcessDependencies).
// release becomes .Release.
func Render(c *chart.Chart, vals map[string]interface{}, release map[string]interface{}, opts Options) (*Result, error) {
	t := template.New("gotpl")
	if opts.Strict {
		t.Option("missingkey=error")
	} else {
		// Not all missing keys are caught by missingkey=zero: Go still emits
		// "<no value>" for some, which is stripped after execution.
		t.Option("missingkey=zero")
	}
	initFuncMap(t, opts)

	for _, tpl := range c.AllTemplates() {
		if _, err := t.New(tpl.Name).Parse(string(tpl.Data)); err != nil {
			return nil, cleanupParseError(tpl.Name, err)
		}
	}

	out := &Result{}
	if err := renderChart(t, c, vals, release, opts, true, out); err != nil {
		return nil, err
	}
	return out, nil
}

func renderChart(t *template.Template, c *chart.Chart, vals, release map[string]interface{}, opts Options, root bool, out *Result) error {
	if vals == nil {
		vals = map[string]interface{}{}
	}
	files := Files(c.Files)
	for _, tpl := range c.Templates {
		isNotes := tpl.Path == "templates/NOTES.txt"
		if isNotes && !root && !opts.SubchartNotes {
			continue
		}
		if tpl.IsPartial() && !isNotes {
			continue
		}
		data := map[string]interface{}{
			"Values":       vals,
			"Chart":        c.Metadata,
			"Release":      release,
			"Files":        files,
			"Capabilities": opts.Capabilities,
			"Template": map[string]interface{}{
				"Name":     tpl.Name,
				"BasePath": strings.TrimSuffix(tpl.Name, "/"+tpl.Path) + "/templates",
			},
		}
		var buf strings.Builder
		if err := t.ExecuteTemplate(&buf, tpl.Name, data); err != nil {
			return cleanupExecError(tpl.Name, err)
		}
		content := strings.ReplaceAll(buf.String(), "<no value>", "")
		if isNotes {
			if strings.TrimSpace(content) != "" {
				if out.Notes != "" {
					out.Notes = strings.TrimRight(out.Notes, "\n") + "\n\n"
				}
				out.Notes += content
			}
			continue
		}
		out.Templates = append(out.Templates, Rendered{Name: tpl.Name, Content: content})
	}
	for _, d := range c.Dependencies {
		if d.IsLibrary() {
			continue
		}
		sub, _ := vals[d.Metadata.Name].(map[string]interface{})
		if err := renderChart(t, d, sub, release, opts, false, out); err != nil {
			return err
		}
	}
	return nil
}

func initFuncMap(t *template.Template, opts Options) {
	f := funcMap(opts.NoEnv, opts.Env)
	includedNames := make(map[string]int)

	f["include"] = includeFun(t, includedNames)
	f["tpl"] = tplFun(t, includedNames, opts.Strict)
	f["required"] = func(warn string, val interface{}) (interface{}, error) {
		if val == nil {
			return val, errors.New(warnWrap(warn))
		}
		if s, ok := val.(string); ok && s == "" {
			return val, errors.New(warnWrap(warn))
		}
		return val, nil
	}
	f["fail"] = func(msg string) (string, error) {
		return "", errors.New(warnWrap(msg))
	}
	f["lookup"] = func(kind, name string) (map[string]interface{}, error) {
		if opts.Lookup == nil {
			return map[string]interface{}{}, nil
		}
		return opts.Lookup(kind, name)
	}
	t.Funcs(f)
}

// includeFun renders a named template to a string so its output can be piped,
// e.g. {{ include "common.labels" . | nindent 4 }}.
func includeFun(t *template.Template, includedNames map[string]int) func(string, interface{}) (string, error) {
	return func(name string, data interface{}) (string, error) {
		var buf strings.Builder
		if v, ok := includedNames[name]; ok {
			if v > recursionMaxNums {
				return "", fmt.Errorf("rendering template has a nested reference name: %s", name)
			}
			includedNames[name]++
		} else {
			includedNames[name] = 1
		}
		err := t.ExecuteTemplate(&buf, name, data)
		includedNames[name]--
		return buf.String(), err
	}
}

// tplFun renders a string as a template against the given data. It clones the
// parent set so defines inside the string can be included and nested tpl calls
// see the defines of their enclosing contexts.
func tplFun(parent *template.Template, includedNames map[string]int, strict bool) func(string, interface{}) (string, error) {
	return func(tpl string, vals interface{}) (string, error) {
		t, err := parent.Clone()
		if err != nil {
			return "", fmt.Errorf("cannot clone template: %w", err)
		}
		// Clone drops the missingkey option (golang/go#43022), so set it again.
		if strict {
			t.Option("missingkey=error")
		} else {
			t.Option("missingkey=zero")
		}
		t.Funcs(template.FuncMap{
			"include": includeFun(t, includedNames),
			"tpl":     tplFun(t, includedNames, strict),
		})

		t, err = t.New(parent.Name()).Parse(tpl)
		if err != nil {
			return "", fmt.Errorf("cannot parse template %q: %w", tpl, err)
		}
		var buf strings.Builder
		if err := t.Execute(&buf, vals); err != nil {
			return "", fmt.Errorf("error during tpl function execution for %q: %w", tpl, err)
		}
		return strings.ReplaceAll(buf.String(), "<no value>", ""), nil
	}
}

func cleanupParseError(filename string, err error) error {
	tokens := strings.Split(err.Error(), ": ")
	if len(tokens) == 1 {
		return fmt.Errorf("parse error in %s: %s", filename, err)
	}
	// tokens[0] is "template", tokens[1] is "file:line[:col]", the last is the cause.
	return fmt.Errorf("parse error at %s: %s", tokens[1], tokens[len(tokens)-1])
}

func cleanupExecError(filename string, err error) error {
	var execErr template.ExecError
	if !errors.As(err, &execErr) {
		return err
	}
	tokens := strings.SplitN(err.Error(), ": ", 3)
	if len(tokens) != 3 {
		return fmt.Errorf("execution error in %s: %s", filename, err)
	}
	if parts := warnRegex.FindStringSubmatch(tokens[2]); len(parts) >= 2 {
		return fmt.Errorf("%s: %s", tokens[1], parts[1])
	}
	return fmt.Errorf("execution error at %s: %s", tokens[1], tokens[2])
}
