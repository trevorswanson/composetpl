/*
Portions adapted from Helm (helm.sh/helm/v3/pkg/engine/funcs.go).
Copyright The Helm Authors. Licensed under the Apache License, Version 2.0.
*/

package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
	goYaml "sigs.k8s.io/yaml/goyaml.v3"
)

// funcMap returns Sprig plus Helm's extra functions. include, tpl, required and fail
// are bound later because they close over the template set and engine options.
func funcMap(noEnv bool) template.FuncMap {
	f := sprig.TxtFuncMap()
	if noEnv {
		delete(f, "env")
		delete(f, "expandenv")
	}

	extra := template.FuncMap{
		"toToml":        toTOML,
		"fromToml":      fromTOML,
		"toYaml":        toYAML,
		"toYamlPretty":  toYAMLPretty,
		"fromYaml":      fromYAML,
		"fromYamlArray": fromYAMLArray,
		"toJson":        toJSON,
		"fromJson":      fromJSON,
		"fromJsonArray": fromJSONArray,
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func toYAML(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

func toYAMLPretty(v interface{}) string {
	var data bytes.Buffer
	encoder := goYaml.NewEncoder(&data)
	encoder.SetIndent(2)
	if err := encoder.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(data.String(), "\n")
}

func fromYAML(str string) map[string]interface{} {
	m := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

func fromYAMLArray(str string) []interface{} {
	a := []interface{}{}
	if err := yaml.Unmarshal([]byte(str), &a); err != nil {
		a = []interface{}{err.Error()}
	}
	return a
}

func toTOML(v interface{}) string {
	b := bytes.NewBuffer(nil)
	if err := toml.NewEncoder(b).Encode(v); err != nil {
		return err.Error()
	}
	return b.String()
}

func fromTOML(str string) map[string]interface{} {
	m := make(map[string]interface{})
	if err := toml.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func fromJSON(str string) map[string]interface{} {
	m := make(map[string]interface{})
	if err := json.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

func fromJSONArray(str string) []interface{} {
	a := []interface{}{}
	if err := json.Unmarshal([]byte(str), &a); err != nil {
		a = []interface{}{err.Error()}
	}
	return a
}
