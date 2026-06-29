// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cmdServiceDiscover returns systemd state for all service units.
func cmdServiceDiscover(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	conn, err := newConn()
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	defer conn.Close()
	units, err := conn.ListUnits()
	if err != nil {
		return failResult("CALLABLE_FAILED", "list units: "+err.Error())
	}
	regs, _ := listRegistered()
	regMap := make(map[string]*registration, len(regs))
	for _, r := range regs {
		regMap[r.name] = r
	}
	type discoverEntry struct {
		Name        string   `json:"name"`
		Load        string   `json:"load"`
		Active      string   `json:"active"`
		Sub         string   `json:"sub"`
		Description string   `json:"description"`
		Registered  bool     `json:"registered"`
		Allowed     []string `json:"allowed,omitempty"`
	}
	var entries []discoverEntry
	for _, u := range units {
		if !strings.HasSuffix(u.Name, ".service") {
			continue
		}
		name := strings.TrimSuffix(u.Name, ".service")
		e := discoverEntry{Name: name, Load: u.LoadState, Active: u.ActiveState, Sub: u.SubState, Description: u.Description}
		if r, ok := regMap[name]; ok {
			e.Registered = true
			e.Allowed = r.operations
		}
		entries = append(entries, e)
	}
	return okResult(entries)
}

func cmdServiceList(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	regs, err := listRegistered()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read services.d: "+err.Error())
	}
	conn, err := newConn()
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	defer conn.Close()
	units, _ := conn.ListUnits()
	unitMap := make(map[string]string, len(units))
	for _, u := range units {
		unitMap[strings.TrimSuffix(u.Name, ".service")] = u.ActiveState
	}
	type entry struct {
		Name    string   `json:"name"`
		Active  string   `json:"active"`
		Allowed []string `json:"allowed"`
	}
	result := make([]entry, 0, len(regs))
	for _, r := range regs {
		active := unitMap[r.name]
		if active == "" {
			active = "unknown"
		}
		result = append(result, entry{Name: r.name, Active: active, Allowed: r.operations})
	}
	return okResult(result)
}

func cmdServiceRegister(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	var ops []string
	if raw, _ := args["operations"].(string); strings.TrimSpace(raw) != "" {
		for _, op := range strings.Split(raw, ",") {
			if o := strings.TrimSpace(op); o != "" {
				ops = append(ops, o)
			}
		}
	} else {
		if conn, err := newConn(); err == nil {
			ops = serviceCapabilities(conn, name)
			conn.Close()
		}
		if len(ops) == 0 {
			ops = defaultOperations
		}
	}
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		return failResult("CALLABLE_FAILED", "create services.d: "+err.Error())
	}
	if err := os.WriteFile(servicesPath(name), []byte(registrationContent(name, ops)), 0644); err != nil {
		return failResult("CALLABLE_FAILED", "write registration: "+err.Error())
	}
	return okResult(map[string]interface{}{"name": name, "operations": ops})
}

func cmdServiceUnregister(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	if err := os.Remove(servicesPath(name)); os.IsNotExist(err) {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("service %q is not registered", name))
	} else if err != nil {
		return failResult("CALLABLE_FAILED", "remove registration: "+err.Error())
	}
	return okResult(map[string]interface{}{"name": name, "unregistered": true})
}

func cmdServiceStatus(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	conn, err := newConn()
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	defer conn.Close()
	props, err := conn.GetUnitProperties(name + ".service")
	if err != nil {
		return failResult("CALLABLE_FAILED", "get unit properties: "+err.Error())
	}
	str := func(key string) string { v, _ := props[key].(string); return v }
	return okResult(map[string]interface{}{
		"name": name, "load": str("LoadState"), "active": str("ActiveState"),
		"sub": str("SubState"), "description": str("Description"), "unit_file": str("UnitFileState"),
	})
}

var dbusSupportedOps = map[string]bool{"start": true, "stop": true, "restart": true, "reload": true, "enable": true, "disable": true}

func cmdServiceControl(ctx context.Context, args map[string]interface{}, op string) (interface{}, *errorDetail) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	reg, err := readRegistration(name)
	if err != nil {
		return failResult("CALLABLE_FAILED", "read registration: "+err.Error())
	}
	if reg == nil {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("service %q is not registered", name))
	}
	if !reg.allows(op) {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("operation %q not permitted for service %q", op, name))
	}
	unit := name + ".service"
	if !dbusSupportedOps[op] {
		out, err := exec.Command("systemctl", op, unit).CombinedOutput()
		if err != nil {
			return failResult("CALLABLE_FAILED", fmt.Sprintf("%s %s: %v\n%s", op, name, err, out))
		}
		return okResult(map[string]interface{}{"name": name, "operation": op})
	}
	conn, err := newConn()
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	defer conn.Close()
	switch op {
	case "enable":
		if _, _, err := conn.EnableUnitFiles([]string{unit}, false, true); err != nil {
			return failResult("CALLABLE_FAILED", "enable: "+err.Error())
		}
	case "disable":
		if _, err := conn.DisableUnitFiles([]string{unit}, false); err != nil {
			return failResult("CALLABLE_FAILED", "disable: "+err.Error())
		}
	default:
		ch := make(chan string, 1)
		var jobErr error
		switch op {
		case "start":
			_, jobErr = conn.StartUnit(unit, "replace", ch)
		case "stop":
			_, jobErr = conn.StopUnit(unit, "replace", ch)
		case "restart":
			_, jobErr = conn.RestartUnit(unit, "replace", ch)
		}
		if jobErr != nil {
			return failResult("CALLABLE_FAILED", op+": "+jobErr.Error())
		}
		if result := <-ch; result != "done" {
			return failResult("CALLABLE_FAILED", fmt.Sprintf("%s %s: job result %q", op, name, result))
		}
	}
	return okResult(map[string]interface{}{"name": name, "operation": op})
}
