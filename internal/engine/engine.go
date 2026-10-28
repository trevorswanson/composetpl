/*
Portions adapted from Helm (helm.sh/helm/v3/pkg/engine/engine.go).
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

	"composetpl/internal/chart"
)

// Options controls rendering behavior.
type Options struct {
	// Strict fails on references to missing map keys instead of rendering them empty.
	Strict bool
	// NoEnv removes the env and expandenv functions, as Helm does.
	NoEnv bool
}

// Rendered is the output of one non-partial template.
type Rendered struct {
	Name    string
	Content string
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
// the root chart's non-partial templates. release becomes .Release.
func Render(c *chart.Chart, vals map[string]interface{}, release map[string]interface{}, opts Options) ([]Rendered, error) {
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

	files := Files(c.Files)
	var out []Rendered
	for _, tpl := range c.Templates {
		if tpl.IsPartial() {
			continue
		}
		data := map[string]interface{}{
			"Values":  vals,
			"Chart":   c.Metadata,
			"Release": release,
			"Files":   files,
			"Template": map[string]interface{}{
				"Name":     tpl.Name,
				"BasePath": c.Metadata.Name + "/templates",
			},
		}
		var buf strings.Builder
		if err := t.ExecuteTemplate(&buf, tpl.Name, data); err != nil {
			return nil, cleanupExecError(tpl.Name, err)
		}
		out = append(out, Rendered{
			Name:    tpl.Name,
			Content: strings.ReplaceAll(buf.String(), "<no value>", ""),
		})
	}
	return out, nil
}

func initFuncMap(t *template.Template, opts Options) {
	f := funcMap(opts.NoEnv)
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
