// SPDX-License-Identifier: Apache-2.0

package module

import (
	"os"
	"path/filepath"
	"strings"
)

// ReadModuleConf reads the module config file at confDir/{shortName}.conf and
// returns its key=value pairs as a map. Lines beginning with # are comments.
// Missing or unreadable files return an empty map — not an error.
func ReadModuleConf(confDir, shortName string) map[string]string {
	path := filepath.Join(confDir, shortName+".conf")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			result[key] = val
		}
	}
	return result
}
