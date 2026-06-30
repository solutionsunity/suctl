// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/solutionsunity/suctl/sdk/surface"
)

// serviceArg resolves the target service name from the standard REPL action
// "subject" key, falling back to "name" for direct (non-surface) invocations.
func serviceArg(args map[string]interface{}) string {
	s, _ := args["subject"].(string)
	if strings.TrimSpace(s) == "" {
		s, _ = args["name"].(string)
	}
	return strings.TrimSpace(s)
}

func cmdServiceRegister(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name := serviceArg(args)
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
	name := serviceArg(args)
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

var dbusSupportedOps = map[string]bool{"start": true, "stop": true, "restart": true, "reload": true, "enable": true, "disable": true}

func cmdServiceControl(ctx context.Context, args map[string]interface{}, op string) (interface{}, *errorDetail) {
	name := serviceArg(args)
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

// ── Service surface (survey / focus) ──────────────────────────────────────────

// cmdServiceSurvey discovers every systemd .service unit and tags each as
// managed (has a suctl registration) or unmanaged — the survey entry for the
// service surface.
func cmdServiceSurvey(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	regs, err := listRegistered()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read services.d: "+err.Error())
	}
	regMap := make(map[string]*registration, len(regs))
	for _, r := range regs {
		regMap[r.name] = r
	}
	conn, err := newConn()
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	defer conn.Close()
	units, err := conn.ListUnits()
	if err != nil {
		return failResult("CALLABLE_FAILED", "list units: "+err.Error())
	}
	var failed, managed int
	subjects := make([]surface.Subject, 0, len(units))
	for _, u := range units {
		if !strings.HasSuffix(u.Name, ".service") {
			continue
		}
		name := strings.TrimSuffix(u.Name, ".service")
		active := u.ActiveState
		if active == "" {
			active = "unknown"
		}
		if active == "failed" {
			failed++
		}
		sub := u.SubState
		if sub == "" {
			sub = "—"
		}
		reg := regMap[name]
		if reg != nil {
			managed++
		}
		subjects = append(subjects, surface.Subject{
			ID:   name,
			Name: name,
			Columns: map[string]surface.Column{
				"active":  surface.Col(active, activeColor(active)),
				"sub":     surface.Col(sub, ""),
				"managed": surface.Col(managedLabel(reg), managedColor(reg)),
			},
			InlineActions: serviceActions(reg, active, false),
			Facets:        []string{serviceFacet(active), managedFacet(reg)},
		})
	}
	summary := fmt.Sprintf("%d managed", managed)
	if failed > 0 {
		summary += fmt.Sprintf(" · %d failed", failed)
	}
	return okResult(surface.SurveyResponse{Total: len(subjects), StatusSummary: summary, Subjects: subjects})
}

// cmdServiceFocus returns one service's full systemd state plus the actions
// valid for its current state — the focus entry for the service surface. An
// unmanaged unit is shown read-only with a register action.
func cmdServiceFocus(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name := serviceArg(args)
	if name == "" {
		return failResult("INVALID_PARAMS", "subject is required")
	}
	reg, err := readRegistration(name)
	if err != nil {
		return failResult("CALLABLE_FAILED", "read registration: "+err.Error())
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
	active := str("ActiveState")
	ops := "— (unmanaged)"
	if reg != nil {
		ops = strings.Join(reg.operations, ", ")
	}
	fields := []surface.Field{
		{Label: "active", Value: active, Color: activeColor(active)},
		{Label: "sub", Value: str("SubState")},
		{Label: "load", Value: str("LoadState")},
		{Label: "unit file", Value: str("UnitFileState")},
		{Label: "managed", Value: managedLabel(reg), Color: managedColor(reg)},
		{Label: "description", Value: str("Description")},
		{Label: "operations", Value: ops},
	}
	return okResult(surface.FocusResponse{
		ID:       name,
		Name:     name,
		Sections: []surface.Section{{Title: "service", Fields: fields}},
		Actions:  serviceActions(reg, active, true),
	})
}

// activeColor maps a systemd ActiveState to a semantic surface color token.
func activeColor(state string) string {
	switch state {
	case "active":
		return "ok"
	case "failed":
		return "err"
	case "activating", "deactivating", "reloading":
		return "warn"
	default:
		return "ghost"
	}
}

// serviceFacet folds the systemd ActiveState into the surface facet vocabulary
// (active / inactive / failed).
func serviceFacet(state string) string {
	switch state {
	case "active":
		return "active"
	case "failed":
		return "failed"
	default:
		return "inactive"
	}
}

// managedFacet maps a registration presence into the management facet
// vocabulary (managed / unmanaged).
func managedFacet(r *registration) string {
	if r == nil {
		return "unmanaged"
	}
	return "managed"
}

// managedLabel / managedColor render the management state as a column/field cell.
func managedLabel(r *registration) string {
	if r == nil {
		return "no"
	}
	return "yes"
}

func managedColor(r *registration) string {
	if r == nil {
		return "ghost"
	}
	return "blue"
}

// serviceActions builds the state-valid action set for a service. An unmanaged
// unit (nil registration) offers only register. A managed unit offers
// restart/stop when active, start otherwise, each gated by the registration's
// allowed ops; the focus view additionally offers unregister.
func serviceActions(r *registration, active string, focus bool) []surface.Action {
	if r == nil {
		return []surface.Action{{Capability: "os.service.register", Label: "register"}}
	}
	var a []surface.Action
	if active == "active" {
		if r.allows("restart") {
			a = append(a, surface.Action{Capability: "os.service.restart", Label: "restart"})
		}
		if r.allows("stop") {
			a = append(a, surface.Action{Capability: "os.service.stop", Label: "stop", Destructive: true})
		}
	} else if r.allows("start") {
		a = append(a, surface.Action{Capability: "os.service.start", Label: "start"})
	}
	if focus {
		a = append(a, surface.Action{Capability: "os.service.unregister", Label: "unregister", Destructive: true})
	}
	return a
}
