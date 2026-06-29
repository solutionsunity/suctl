// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sysdbus "github.com/coreos/go-systemd/v22/dbus"
)

const servicesDir = "/etc/suctl/services.d"

// registration represents a parsed services.d drop-in file.
type registration struct {
	name       string
	operations []string
}

func (r *registration) allows(op string) bool {
	for _, o := range r.operations {
		if o == op {
			return true
		}
	}
	return false
}

var defaultOperations = []string{"start", "stop", "restart", "reload", "enable", "disable"}

func newConn() (*sysdbus.Conn, error) {
	conn, err := sysdbus.NewSystemConnection()
	if err != nil {
		return nil, fmt.Errorf("connect to systemd D-Bus: %w", err)
	}
	return conn, nil
}

func servicesPath(name string) string { return filepath.Join(servicesDir, name+".conf") }

func registrationContent(name string, ops []string) string {
	return fmt.Sprintf("[service.%s]\noperations = %s\n", name, strings.Join(ops, ", "))
}

func parseRegistration(name string, data []byte) *registration {
	reg := &registration{name: name}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "operations" {
			for _, op := range strings.Split(strings.TrimSpace(parts[1]), ",") {
				if o := strings.TrimSpace(op); o != "" {
					reg.operations = append(reg.operations, o)
				}
			}
		}
	}
	return reg
}

func readRegistration(name string) (*registration, error) {
	data, err := os.ReadFile(servicesPath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseRegistration(name, data), nil
}

func listRegistered() ([]*registration, error) {
	entries, err := os.ReadDir(servicesDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []*registration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".conf")
		data, err := os.ReadFile(filepath.Join(servicesDir, e.Name()))
		if err != nil {
			continue
		}
		result = append(result, parseRegistration(name, data))
	}
	return result, nil
}

func serviceCapabilities(conn *sysdbus.Conn, name string) []string {
	props, err := conn.GetUnitProperties(name + ".service")
	if err != nil {
		return nil
	}
	boolProp := func(key string) bool { v, _ := props[key].(bool); return v }
	canStart := boolProp("CanStart")
	canStop := boolProp("CanStop")
	var ops []string
	if canStart {
		ops = append(ops, "start")
	}
	if canStop {
		ops = append(ops, "stop")
	}
	if canStart && canStop {
		ops = append(ops, "restart")
	}
	if boolProp("CanReload") {
		ops = append(ops, "reload")
	}
	ops = append(ops, "enable", "disable")
	return ops
}
