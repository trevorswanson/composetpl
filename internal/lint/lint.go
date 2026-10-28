// Package lint performs structural checks on a merged compose document.
package lint

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

var knownTopLevel = map[string]bool{
	"name": true, "services": true, "networks": true, "volumes": true,
	"configs": true, "secrets": true, "include": true, "models": true,
}

// Result holds lint findings. Errors fail the lint; warnings do not.
type Result struct {
	Errors   []string
	Warnings []string
}

// Check validates the top-level keys and services of a merged compose mapping.
func Check(root *yaml.Node) Result {
	var r Result
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		switch {
		case key == "version":
			r.Warnings = append(r.Warnings, "top-level `version` is obsolete in the Compose Specification and is ignored")
		case strings.HasPrefix(key, "x-") || knownTopLevel[key]:
		default:
			r.Errors = append(r.Errors, fmt.Sprintf("line %d: unknown top-level key %q", root.Content[i].Line, key))
		}
		if key == "services" {
			r.checkServices(val)
		}
	}
	return r
}

func (r *Result) checkServices(services *yaml.Node) {
	if services.Kind != yaml.MappingNode {
		r.Errors = append(r.Errors, fmt.Sprintf("line %d: services must be a mapping", services.Line))
		return
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			r.Errors = append(r.Errors, fmt.Sprintf("service %q: must be a mapping", name))
			continue
		}
		if !hasKey(svc, "image") && !hasKey(svc, "build") && !hasKey(svc, "extends") {
			r.Errors = append(r.Errors, fmt.Sprintf("service %q: needs image, build or extends", name))
		}
	}
}

func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// DockerCompose runs `docker compose config -q` against doc when docker compose is
// installed. It returns ran=false when docker or its compose plugin is missing.
func DockerCompose(doc []byte) (ran bool, err error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return false, nil
	}
	if exec.Command(docker, "compose", "version").Run() != nil {
		return false, nil
	}
	cmd := exec.Command(docker, "compose", "-f", "-", "config", "-q")
	cmd.Stdin = bytes.NewReader(doc)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return true, fmt.Errorf("docker compose config: %s", strings.TrimSpace(stderr.String()))
	}
	return true, nil
}
