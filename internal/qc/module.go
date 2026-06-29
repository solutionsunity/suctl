// SPDX-License-Identifier: Apache-2.0

package qc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func runModule(dir string) {
	var checks []Check

	checks = append(checks, checkMQ01(dir))
	// If MQ01 fails, we can't do much more with JSON
	if checks[len(checks)-1].Result == Blocked {
		Report(checks)
		return
	}

	checks = append(checks, checkMQ02(dir))
	checks = append(checks, checkMQ03(dir))
	checks = append(checks, checkMQ04(dir))
	checks = append(checks, checkMQ05(dir))

	Report(checks)
}

func checkMQ01(dir string) Check {
	path := filepath.Join(dir, "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return Check{ID: "MQ01", Name: "manifest.json valid", Result: Blocked, Message: err.Error()}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return Check{ID: "MQ01", Name: "manifest.json valid", Result: Blocked, Message: "invalid JSON: " + err.Error()}
	}
	return Check{ID: "MQ01", Name: "manifest.json valid", Result: Pass}
}

func checkMQ02(dir string) Check {
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)

	required := []string{"version", "protocol", "platform", "author", "license", "entrypoint", "description", "capabilities"}
	var missing []string
	for _, field := range required {
		if _, ok := m[field]; !ok {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		return Check{ID: "MQ02", Name: "Required manifest fields present", Result: Blocked, Message: "missing fields: " + strings.Join(missing, ", ")}
	}
	return Check{ID: "MQ02", Name: "Required manifest fields present", Result: Pass}
}

func checkMQ03(dir string) Check {
	// Identity is the module directory name — suctl-mod-{name}.
	name := filepath.Base(dir)
	if !strings.HasPrefix(name, "suctl-mod-") {
		return Check{ID: "MQ03", Name: "Module directory name convention", Result: Blocked, Message: "directory must be named suctl-mod-{name}"}
	}
	suffix := strings.TrimPrefix(name, "suctl-mod-")
	if suffix == "" {
		return Check{ID: "MQ03", Name: "Module directory name convention", Result: Blocked, Message: "name after suctl-mod- is empty"}
	}
	for _, r := range suffix {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return Check{ID: "MQ03", Name: "Module directory name convention", Result: Blocked, Message: "must be lowercase alphanumeric and hyphens, no underscores"}
		}
	}
	return Check{ID: "MQ03", Name: "Module directory name convention", Result: Pass}
}

func checkMQ04(dir string) Check {
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)

	protocol, ok := m["protocol"].(string)
	if !ok {
		return Check{ID: "MQ04", Name: "Protocol version", Result: Blocked, Message: "protocol must be a string"}
	}
	if protocol != "1" {
		return Check{ID: "MQ04", Name: "Protocol version", Result: Blocked, Message: "unsupported protocol version " + protocol}
	}
	return Check{ID: "MQ04", Name: "Protocol version", Result: Pass}
}

func checkMQ05(dir string) Check {
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)

	platform, ok := m["platform"].([]interface{})
	if !ok {
		return Check{ID: "MQ05", Name: "Platform is an explicit array", Result: Blocked, Message: "platform must be an array"}
	}
	if len(platform) == 0 {
		return Check{ID: "MQ05", Name: "Platform is an explicit array", Result: Blocked, Message: "platform array must not be empty"}
	}
	return Check{ID: "MQ05", Name: "Platform is an explicit array", Result: Pass}
}
