// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/brokerclient"
	"github.com/solutionsunity/suctl/sdk/modserver"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
)

// ── Reload helper ─────────────────────────────────────────────────────────────

// reloadOrPartial calls nginxReload after a successful config write/remove.
// When reload fails it returns a CALLABLE_FAILED error that clearly signals the
// on-disk state *was* updated but the running nginx was NOT reloaded — the
// operator must fix nginx and reload manually to resolve the drift.
func reloadOrPartial(action string) *errorDetail {
	if err := nginxReload(); err != nil {
		return &errorDetail{
			Code:    "CALLABLE_FAILED",
			Message: fmt.Sprintf("config change applied but nginx reload failed (%s) — running nginx still serves previous state: %v", action, err),
		}
	}
	return nil
}

// ── Domain operations ─────────────────────────────────────────────────────────

func cmdDomainAdd(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	domain, ok := requireDomainArg(args)
	if !ok {
		return failResult("INVALID_PARAMS", "domain is required")
	}
	if existing, _ := parseNginxTree(); existing != nil {
		for _, b := range existing {
			if !isSuctlOverride(b) && domainInBlock(domain, b.serverNames) {
				return failResult("CALLABLE_FAILED", fmt.Sprintf("domain %q is already configured in nginx", domain))
			}
		}
	}
	www := strings.TrimSpace(fmt.Sprintf("%v", args["www"])) == "true"
	serverNames := domain
	if www {
		serverNames = domain + " www." + domain
	}
	conf := fmt.Sprintf(serverBlockTemplate, serverNames)
	if err := os.MkdirAll(sitesAvailableDir, 0755); err != nil {
		return failResult("INTERNAL_ERROR", "create sites-available: "+err.Error())
	}
	if err := os.WriteFile(sitesAvailablePath(domain), []byte(conf), 0644); err != nil {
		return failResult("INTERNAL_ERROR", "write server block: "+err.Error())
	}
	if err := os.MkdirAll(sitesEnabledDir, 0755); err != nil {
		return failResult("INTERNAL_ERROR", "create sites-enabled: "+err.Error())
	}
	os.Remove(sitesEnabledPath(domain)) //nolint:errcheck
	if err := os.Symlink(sitesAvailablePath(domain), sitesEnabledPath(domain)); err != nil {
		return failResult("INTERNAL_ERROR", "enable site: "+err.Error())
	}
	if ed := reloadOrPartial("domain.add"); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"domain": domain, "www": www, "action": "added"})
}

// provisionRollback undoes the fresh server block that cmdProvision just added
// (a single per-domain file plus its sites-enabled symlink) when a later
// provisioning step fails. It is intentionally minimal — it removes only the
// artifacts provision itself created — and is deliberately not an operator-
// facing capability: operator-driven domain removal is manual for now.
func provisionRollback(domain string) {
	os.Remove(sitesEnabledPath(domain))   //nolint:errcheck
	os.Remove(sitesAvailablePath(domain)) //nolint:errcheck
	_ = nginxReload()
}

func cmdDomainList(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	metas, err := ListDomains()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read domains: "+err.Error())
	}
	return okResult(metas)
}

// ── State operations ──────────────────────────────────────────────────────────

func cmdSuspend(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	domain, ok := requireDomainArg(args)
	if !ok {
		return failResult("INVALID_PARAMS", "domain is required")
	}
	m, err := ReadDomain(domain)
	if err != nil {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("domain %q not found", domain))
	}
	if err := writeSuspensionConf(m); err != nil {
		return failResult("INTERNAL_ERROR", "write suspension conf: "+err.Error())
	}
	if ed := reloadOrPartial("domain.suspend"); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"domain": domain, "suspended": true})
}

func cmdUnsuspend(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	domain, ok := requireDomainArg(args)
	if !ok {
		return failResult("INVALID_PARAMS", "domain is required")
	}
	m, err := ReadDomain(domain)
	if err != nil {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("domain %q not found", domain))
	}
	os.Remove(suspensionConfPath(domain)) //nolint:errcheck
	if ed := reloadOrPartial("domain.unsuspend"); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"domain": domain, "suspended": false, "maintenance": m.Maintenance})
}

func cmdMaintenance(ctx context.Context, args map[string]interface{}, on bool) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	all := strings.TrimSpace(fmt.Sprintf("%v", args["all"])) == "true"
	if all {
		return applyMaintenanceAll(on)
	}
	domain, ok := requireDomainArg(args)
	if !ok {
		return failResult("INVALID_PARAMS", "domain is required (or set all=true)")
	}
	m, err := ReadDomain(domain)
	if err != nil {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("domain %q not found", domain))
	}
	if on {
		if err := writeMaintenanceConf(m); err != nil {
			return failResult("INTERNAL_ERROR", "write maintenance conf: "+err.Error())
		}
	} else {
		os.Remove(maintenanceConfPath(domain)) //nolint:errcheck
	}
	action := "maintenance.enable"
	if !on {
		action = "maintenance.disable"
	}
	if ed := reloadOrPartial(action); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"domain": domain, "maintenance": on, "suspended": m.Suspended})
}

func applyMaintenanceAll(on bool) (interface{}, *errorDetail) {
	metas, err := ListDomains()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read domains: "+err.Error())
	}
	var failed []string
	affected := 0
	for _, m := range metas {
		if m.Role == RoleRedirect {
			continue
		}
		affected++
		if on {
			if err := writeMaintenanceConf(m); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", m.Domain, err))
			}
		} else {
			os.Remove(maintenanceConfPath(m.Domain)) //nolint:errcheck
		}
	}
	if affected == 0 {
		return okResult(map[string]interface{}{"affected": 0, "maintenance": on})
	}
	if len(failed) > 0 {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("maintenance toggle failed for %d domain(s): %s", len(failed), strings.Join(failed, "; ")))
	}
	allAction := "maintenance.enable all"
	if !on {
		allAction = "maintenance.disable all"
	}
	if ed := reloadOrPartial(allAction); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"affected": affected, "maintenance": on})
}

// ── Conf writers ──────────────────────────────────────────────────────────────

func writeSuspensionConf(m *DomainInfo) error {
	if err := os.MkdirAll(confdDir, 0755); err != nil {
		return err
	}
	serverNames := strings.Join(m.ServerNames, " ")
	if serverNames == "" {
		serverNames = m.Domain
	}
	var conf string
	if hasLiveCert(m.Domain) {
		conf = fmt.Sprintf(suspendedBlockHTTPS, serverNames, serverNames, m.Domain, m.Domain)
	} else {
		conf = fmt.Sprintf(suspendedBlockHTTP, serverNames)
	}
	return os.WriteFile(suspensionConfPath(m.Domain), []byte(conf), 0644)
}

func writeMaintenanceConf(m *DomainInfo) error {
	if err := os.MkdirAll(confdDir, 0755); err != nil {
		return err
	}
	serverNames := strings.Join(m.ServerNames, " ")
	if serverNames == "" {
		serverNames = m.Domain
	}
	var conf string
	if hasLiveCert(m.Domain) {
		conf = fmt.Sprintf(maintenanceBlockHTTPS, serverNames, serverNames, m.Domain, m.Domain)
	} else {
		conf = fmt.Sprintf(maintenanceBlockHTTP, serverNames)
	}
	return os.WriteFile(maintenanceConfPath(m.Domain), []byte(conf), 0644)
}

// ── REPL operations ───────────────────────────────────────────────────────────

func cmdReload(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	if ed := reloadOrPartial("reload"); ed != nil {
		return nil, ed
	}
	return okResult(map[string]interface{}{"reloaded": true})
}

// domainFacets returns the facet tags that apply to this domain row (D68).
// Tags cover both the domain status and the SSL state, so core can filter on
// either dimension (or both) without a round-trip to the module.
func domainFacets(m *DomainInfo, sslState string) []string {
	tags := []string{string(m.Status)}
	if sslState != "" {
		tags = append(tags, "ssl:"+sslState)
	}
	if isDefaultFile(m.ConfigFile) {
		tags = append(tags, "in-default")
	}
	return tags
}

// certEntry is the join shape pulled from certbot.cert.list output.
type certEntry struct {
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
	DaysLeft int      `json:"days_left"`
	Status   string   `json:"status"`
}

// fetchCertsByDomain calls certbot.cert.list over the broker wire and
// returns a map from every SAN domain → cert entry. A nil map is returned
// (and the error suppressed) when the certbot module is unavailable; the
// nginx survey degrades gracefully to showing ssl=none for every row.
func fetchCertsByDomain(ctx context.Context) map[string]*certEntry {
	resp, err := brokerclient.InvokeContext(ctx, "certbot.cert.list", map[string]interface{}{})
	if err != nil || resp == nil {
		return nil
	}
	var inv protocol.InvokeResponse
	if err := json.Unmarshal(resp.Result, &inv); err != nil || inv.Output == nil {
		return nil
	}
	var wrap struct {
		Certificates []*certEntry `json:"certificates"`
	}
	if err := json.Unmarshal(inv.Output, &wrap); err != nil {
		return nil
	}
	out := make(map[string]*certEntry, len(wrap.Certificates))
	for _, c := range wrap.Certificates {
		for _, d := range c.Domains {
			out[d] = c
		}
	}
	return out
}

func cmdReplSurvey(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	all, err := ListDomains()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read domains: "+err.Error())
	}
	total := len(all)

	certs := fetchCertsByDomain(ctx)

	var conflict int
	for _, m := range all {
		if m.Status == StatusConflict {
			conflict++
		}
	}

	var configErr string
	if err := nginxTest(); err != nil {
		configErr = "config invalid"
	}

	// Resolve every domain's DNS once, concurrently under one bounded deadline so
	// a slow resolver cannot hang the survey. This survey is async (manifest):
	// core holds the spinner until the result is pushed, so the dns column and the
	// dns-elsewhere summary are both sourced from this single pass.
	dnsCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dnsState := make(map[string]string, len(all))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range all {
		wg.Add(1)
		go func(m *DomainInfo) {
			defer wg.Done()
			state, _ := resolveDNS(dnsCtx, m.ServerNames)
			mu.Lock()
			dnsState[m.Domain] = state
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	var dnsElsewhere, dnsUnresolved int
	subjects := make([]surface.Subject, 0, len(all))
	for _, m := range all {
		sslVal, sslColor, sslState := sslCell(m, certs)
		blocksVal, blocksColor := blocksCell(m)
		state := dnsState[m.Domain]
		switch state {
		case "elsewhere":
			dnsElsewhere++
		case "unresolved", "":
			dnsUnresolved++
		}
		subjects = append(subjects, surface.Subject{
			ID:   m.Domain,
			Name: m.Domain,
			Columns: map[string]surface.Column{
				"blocks": surface.Col(blocksVal, blocksColor),
				"dns":    dnsColumn(state),
				"ssl":    surface.Col(sslVal, sslColor),
				"status": surface.Col(string(m.Status), domainStatusColor(m.Status)),
			},
			InlineActions: inlineActionsFor(m),
			Facets:        domainFacets(m, sslState),
		})
	}

	return okResult(surface.SurveyResponse{
		Total:         total,
		StatusSummary: domainStatusSummary(conflict, configErr, dnsElsewhere, dnsUnresolved),
		Subjects:      subjects,
	})
}

func cmdReplFocus(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	domain, _ := args["subject"].(string)
	if domain == "" {
		domain, _ = args["domain"].(string)
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return failResult("INVALID_PARAMS", "subject is required")
	}

	m, err := ReadDomain(domain)
	if err != nil {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("domain %q not found", domain))
	}

	certs := fetchCertsByDomain(ctx)
	sslVal, sslColor, _ := sslCell(m, certs)

	// DNS is resolved here — focus, one subject, on demand — never in the survey,
	// which would fire a lookup per row on every render. Bounded so a slow or
	// unreachable resolver cannot hang the focus view.
	dnsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dnsVal, dnsColor := dnsCell(resolveDNS(dnsCtx, m.ServerNames))

	fields := []surface.Field{
		{Label: "status", Value: string(m.Status), Color: domainStatusColor(m.Status)},
		{Label: "role", Value: string(m.Role)},
		{Label: "server_names", Value: strings.Join(m.ServerNames, " ")},
	}
	if m.Role == RoleRedirect && m.RedirectTo != "" {
		fields = append(fields, surface.Field{Label: "redirect", Value: m.RedirectTo, Color: "blue"})
	}
	fields = append(fields,
		surface.Field{Label: "dns", Value: dnsVal, Color: dnsColor},
		surface.Field{Label: "www", Value: string(m.WWWMode)},
		surface.Field{Label: "ssl", Value: sslVal, Color: sslColor},
		surface.Field{Label: "config", Value: m.ConfigFile},
	)
	sections := []surface.Section{
		{Title: "domain", Fields: fields},
	}
	if m.Status == StatusConflict {
		sections = append(sections, conflictSection(m))
	}

	return okResult(surface.FocusResponse{
		ID:       m.Domain,
		Name:     m.Domain,
		Sections: sections,
		Actions:  focusDomainActions(m),
	})
}

// inlineActionsFor mirrors the focus actions that are state-safe to expose on
// the survey row.
func inlineActionsFor(m *DomainInfo) []surface.Action {
	if m.Role == RoleRedirect {
		return nil
	}
	switch m.Status {
	case StatusActive:
		return []surface.Action{
			{Capability: "nginx.domain.suspend", Label: "suspend"},
			{Capability: "nginx.maintenance.enable", Label: "maint on"},
		}
	case StatusSuspended:
		return []surface.Action{
			{Capability: "nginx.domain.unsuspend", Label: "unsuspend"},
		}
	case StatusMaintenance:
		return []surface.Action{
			{Capability: "nginx.maintenance.disable", Label: "maint off"},
		}
	}
	return nil
}

// focusDomainActions builds dynamic focus actions based on the domain state.
// Domain removal is intentionally not offered — operators remove domains
// manually for now — so redirect and conflict blocks expose no focus actions.
func focusDomainActions(m *DomainInfo) []surface.Action {
	if m.Role == RoleRedirect {
		return nil
	}
	switch m.Status {
	case StatusActive:
		return []surface.Action{
			{Capability: "nginx.domain.suspend", Label: "suspend"},
			{Capability: "nginx.maintenance.enable", Label: "maintenance on"},
		}
	case StatusSuspended:
		return []surface.Action{
			{Capability: "nginx.domain.unsuspend", Label: "unsuspend"},
		}
	case StatusMaintenance:
		return []surface.Action{
			{Capability: "nginx.maintenance.disable", Label: "maintenance off"},
		}
	}
	return nil
}

// ── Compound capability ───────────────────────────────────────────────────────

// cmdProvision implements nginx.provision: a compound capability composed in
// code from other capabilities. It handles its own sequence and rollback.
func cmdProvision(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	domain, _ := args["domain"].(string)
	www := strings.TrimSpace(fmt.Sprintf("%v", args["www"])) == "true"
	email, _ := args["ssl_certificate_email"].(string)

	if domain == "" || email == "" {
		return failResult("INVALID_PARAMS", "domain and ssl_certificate_email are required")
	}

	token := modserver.JobToken(ctx)

	// 1. Add domain
	brokerclient.JobUpdate(token, protocol.JobUpdateParams{
		Progress: 10,
		Message:  "Configuring nginx server block...",
	})
	_, ed := cmdDomainAdd(ctx, map[string]interface{}{"domain": domain, "www": www})
	if ed != nil {
		return nil, ed
	}

	// Cleanup on failure — undo only the block provision just added.
	rollback := func() {
		provisionRollback(domain)
	}

	// 2. Provision certificate (cross-module)
	brokerclient.JobUpdate(token, protocol.JobUpdateParams{
		Progress: 40,
		Message:  "Provisioning SSL certificate via certbot...",
	})
	ir, err := brokerclient.InvokeAndWaitContext(ctx, "certbot.cert.provision", map[string]interface{}{
		"domain": domain,
		"email":  email,
	})
	if err != nil {
		rollback()
		// err is already *protocol.ErrorDetail if it's a protocol error
		if ed, ok := err.(*protocol.ErrorDetail); ok {
			return nil, ed
		}
		return failResult("CALLABLE_FAILED", "certbot provision: "+err.Error())
	}
	_ = ir // Output is currently ignored, but we know it's success if err == nil

	// 3. Reload nginx
	brokerclient.JobUpdate(token, protocol.JobUpdateParams{
		Progress: 90,
		Message:  "Finalizing nginx configuration...",
	})
	if _, ed := cmdReload(ctx, map[string]interface{}{}); ed != nil {
		rollback()
		return nil, ed
	}

	brokerclient.JobUpdate(token, protocol.JobUpdateParams{
		Progress: 100,
		Message:  "Provisioning complete.",
	})

	return okResult(map[string]interface{}{"provisioned": true})
}

// ── Color / cell helpers ──────────────────────────────────────────────────────

func domainStatusColor(s Status) string {
	switch s {
	case StatusActive:
		return "ok"
	case StatusSuspended:
		return "warn"
	case StatusMaintenance:
		return "blue"
	case StatusConflict:
		return "err"
	}
	return ""
}

// blocksCell renders the block's composition: its server_name list, or the
// redirect target when the block is a pure 301/302 redirect.
func blocksCell(m *DomainInfo) (val, color string) {
	if m.Role == RoleRedirect {
		if m.RedirectTo != "" {
			return "→ " + m.RedirectTo, "blue"
		}
		return "redirect", "blue"
	}
	return strings.Join(m.ServerNames, " "), ""
}

// sslCell joins the certbot snapshot into a single cell: "valid · 78d",
// "expiring · 9d", "expired · -5d", or "none". The returned sslState is the
// short token (valid/expiring/expired/none) used for facet matching.
func sslCell(m *DomainInfo, certs map[string]*certEntry) (val, color, sslState string) {
	var c *certEntry
	if certs != nil {
		for _, n := range m.ServerNames {
			if cc := certs[n]; cc != nil {
				c = cc
				break
			}
		}
	}
	if c == nil {
		return "none", "ghost", "none"
	}
	state := c.Status
	if state == "" {
		state = "valid"
	}
	switch state {
	case "valid":
		color = "ok"
	case "expiring":
		color = "warn"
	case "expired", "missing":
		color = "err"
	}
	return fmt.Sprintf("%s · %dd", state, c.DaysLeft), color, state
}

// dnsCell renders the resolveDNS tri-state into a focus field value/color. It is
// advisory only (a proxy/LB in front of the host reads as "elsewhere").
func dnsCell(state, detail string) (val, color string) {
	switch state {
	case "local":
		return "local", "ok"
	case "elsewhere":
		if detail != "" {
			return "elsewhere → " + detail, "warn"
		}
		return "elsewhere", "warn"
	default:
		return "unresolved", "ghost"
	}
}

// dnsColumn renders the resolveDNS tri-state into the survey 'dns' column cell.
// Unlike dnsCell (focus) it carries no detail and folds "local" into "ok" — the
// three tokens are ok/elsewhere/unresolved. Advisory only.
func dnsColumn(state string) surface.Column {
	switch state {
	case "local":
		return surface.Col("ok", "ok")
	case "elsewhere":
		return surface.Col("elsewhere", "warn")
	default:
		return surface.Col("unresolved", "ghost")
	}
}


// domainStatusSummary builds the module-level status summary shown on the HOME
// page. Only conditions that need operator attention are included:
//   - conflicts: two blocks claiming the same server_name (config ambiguity)
//   - configErr: nginx -t reports the on-disk config is invalid
//   - dnsElsewhere: domains whose server_names resolve only off this host
//   - dnsUnresolved: domains whose server_names could not be resolved at all
//
// Intentionally omitted: suspended and maintenance — those are deliberate
// operator-chosen states, not faults.
func domainStatusSummary(conflict int, configErr string, dnsElsewhere, dnsUnresolved int) string {
	var parts []string
	if conflict > 0 {
		parts = append(parts, fmt.Sprintf("%d conflict", conflict))
	}
	if configErr != "" {
		parts = append(parts, configErr)
	}
	if dnsElsewhere > 0 {
		parts = append(parts, fmt.Sprintf("%d dns elsewhere", dnsElsewhere))
	}
	if dnsUnresolved > 0 {
		parts = append(parts, fmt.Sprintf("%d dns unresolved", dnsUnresolved))
	}
	return strings.Join(parts, " · ")
}

// conflictSection lists this block plus every other block that collides with
// it (same server_name on the same listen) so the operator can resolve it.
func conflictSection(m *DomainInfo) surface.Section {
	fields := make([]surface.Field, 0, len(m.ConflictWith)+1)
	fields = append(fields, surface.Field{
		Label: "this block",
		Value: fmt.Sprintf("%s — %s", strings.Join(m.ServerNames, " "), m.ConfigFile),
		Color: "err",
	})
	for i, b := range m.ConflictWith {
		fields = append(fields, surface.Field{
			Label: fmt.Sprintf("collides %d", i+1),
			Value: fmt.Sprintf("%s — %s", strings.Join(b.ServerNames, " "), b.SourceFile),
			Color: "err",
		})
	}
	return surface.Section{Title: "conflict", Fields: fields}
}

// ── Source surface (config files) ─────────────────────────────────────────────

func cmdSourceSurvey(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	files, err := ListSources()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read sources: "+err.Error())
	}
	migr := 0
	subjects := make([]surface.Subject, 0, len(files))
	for _, sf := range files {
		migr += sf.Migratable
		subjects = append(subjects, surface.Subject{
			ID:   sf.Name,
			Name: sf.Name,
			Columns: map[string]surface.Column{
				"blocks":  surface.Col(fmt.Sprintf("%d", len(sf.Blocks)), ""),
				"managed": surface.Col(managedCell(sf)),
				"enabled": surface.Col(enabledCell(sf)),
				"migrate": surface.Col(migrateCell(sf)),
			},
			InlineActions: sourceMigrateActions(sf),
			Facets:        sourceFacets(sf),
		})
	}
	summary := ""
	if migr > 0 {
		summary = fmt.Sprintf("%d block(s) to migrate from default", migr)
	}
	return okResult(surface.SurveyResponse{Total: len(files), StatusSummary: summary, Subjects: subjects})
}

func cmdSourceFocus(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["subject"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "subject is required")
	}
	sf, err := ReadSource(name)
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	fields := []surface.Field{
		{Label: "path", Value: sf.Path},
		{Label: "managed", Value: boolWord(sf.Managed, "suctl", "foreign")},
		{Label: "enabled", Value: boolWord(sf.Enabled, "yes", "no")},
		{Label: "blocks", Value: fmt.Sprintf("%d", len(sf.Blocks))},
	}
	if sf.IsDefault {
		fields = append(fields, surface.Field{Label: "to migrate", Value: fmt.Sprintf("%d", sf.Migratable), Color: "warn"})
	}
	sections := []surface.Section{{Title: "file", Fields: fields}}
	if bf := sourceBlockFields(sf); len(bf) > 0 {
		sections = append(sections, surface.Section{Title: "server blocks", Fields: bf})
	}
	return okResult(surface.FocusResponse{
		ID:       sf.Name,
		Name:     sf.Name,
		Sections: sections,
		Actions:  sourceMigrateActions(sf),
	})
}

// cmdBlockSurvey is the drill child of the source surface: it lists the server
// blocks contained in one config file (scoped by the parent file's name).
func cmdBlockSurvey(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["scope"].(string)
	if name == "" {
		name, _ = args["subject"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "scope is required")
	}
	sf, err := ReadSource(name)
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	certs := fetchCertsByDomain(ctx)
	subjects := make([]surface.Subject, 0, len(sf.Blocks))
	for _, b := range sf.Blocks {
		id, nm := b.Primary, b.Primary
		if id == "" {
			id, nm = "_", "(catch-all)"
		}
		sslVal, sslColor := blockSSLCell(b, certs)
		var inline []surface.Action
		if b.Migratable {
			inline = []surface.Action{{Capability: "nginx.source.migrate", Label: "migrate"}}
		}
		subjects = append(subjects, surface.Subject{
			ID:   id,
			Name: nm,
			Columns: map[string]surface.Column{
				"names": surface.Col(strings.Join(b.ServerNames, " "), ""),
				"role":  surface.Col(string(b.Role), roleColor(b.Role)),
				"ssl":   surface.Col(sslVal, sslColor),
			},
			InlineActions: inline,
		})
	}
	return okResult(surface.SurveyResponse{Total: len(sf.Blocks), Subjects: subjects})
}

// cmdSourceMigrate lifts blocks out of the default site. When the subject is the
// default file (the file-row action) every migratable block moves; otherwise the
// subject is a block primary (the per-block drill action) and only it moves.
func cmdSourceMigrate(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireNginx(); ed != nil {
		return nil, ed
	}
	subj, _ := args["subject"].(string)
	subj = strings.TrimSpace(subj)
	all := strings.TrimSpace(fmt.Sprintf("%v", args["all"])) == "true"
	if isDefaultFile(subj) {
		all, subj = true, ""
	}
	if !all && subj == "" {
		return failResult("INVALID_PARAMS", "subject (block) is required, or set all=true")
	}
	res, err := MigrateFromDefault(subj, all)
	if err != nil {
		return failResult("CALLABLE_FAILED", err.Error())
	}
	// Reload only when something actually moved; an all-skipped run left the
	// running config untouched, so a reload would be a no-op.
	if len(res.Migrated) > 0 {
		if ed := reloadOrPartial("source.migrate"); ed != nil {
			return nil, ed
		}
	}
	return okResult(map[string]interface{}{
		"migrated": res.Migrated,
		"count":    len(res.Migrated),
		"skipped":  res.Skipped,
	})
}

// ── Source cell / action helpers ──────────────────────────────────────────────

func managedCell(sf *SourceFile) (string, string) {
	if sf.Managed {
		return "suctl", "ok"
	}
	return "foreign", "ghost"
}

func enabledCell(sf *SourceFile) (string, string) {
	if sf.Enabled {
		return "enabled", "ok"
	}
	return "disabled", "warn"
}

func migrateCell(sf *SourceFile) (string, string) {
	if sf.IsDefault && sf.Migratable > 0 {
		return fmt.Sprintf("%d", sf.Migratable), "warn"
	}
	return "—", "ghost"
}

func sourceFacets(sf *SourceFile) []string {
	var f []string
	if sf.IsDefault {
		f = append(f, "default")
	}
	f = append(f, boolWord(sf.Managed, "managed", "foreign"))
	f = append(f, boolWord(sf.Enabled, "enabled", "disabled"))
	if sf.Migratable > 0 {
		f = append(f, "to-migrate")
	}
	return f
}

func sourceMigrateActions(sf *SourceFile) []surface.Action {
	if sf.IsDefault && sf.Migratable > 0 {
		return []surface.Action{{Capability: "nginx.source.migrate", Label: "migrate all"}}
	}
	return nil
}

func sourceBlockFields(sf *SourceFile) []surface.Field {
	fields := make([]surface.Field, 0, len(sf.Blocks))
	for _, b := range sf.Blocks {
		label := b.Primary
		if label == "" {
			label = "(catch-all)"
		}
		color := ""
		if b.Migratable {
			color = "warn"
		}
		fields = append(fields, surface.Field{Label: label, Value: strings.Join(b.ServerNames, " "), Color: color})
	}
	return fields
}

func blockSSLCell(b SourceBlock, certs map[string]*certEntry) (string, string) {
	if certs != nil {
		for _, n := range b.ServerNames {
			if c := certs[n]; c != nil {
				st := c.Status
				if st == "" {
					st = "valid"
				}
				color := ""
				switch st {
				case "valid":
					color = "ok"
				case "expiring":
					color = "warn"
				case "expired", "missing":
					color = "err"
				}
				return fmt.Sprintf("%s · %dd", st, c.DaysLeft), color
			}
		}
	}
	if b.SSL {
		return "configured", "ghost"
	}
	return "none", "ghost"
}

func roleColor(r BlockRole) string {
	if r == RoleRedirect {
		return "blue"
	}
	return ""
}

func boolWord(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
