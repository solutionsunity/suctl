// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// WWWMode describes how the www variant is handled.
type WWWMode string

const (
	WWWNone      WWWMode = "none"
	WWWSameBlock WWWMode = "same-block"
	WWWRedirect  WWWMode = "redirect"
)

// Status is the canonical availability state of a domain.
type Status string

const (
	StatusActive      Status = "active"
	StatusSuspended   Status = "suspended"
	StatusMaintenance Status = "maintenance"
	StatusConflict    Status = "conflict"
)

// BlockRole distinguishes a real serving block from a pure redirect block.
type BlockRole string

const (
	RoleServing  BlockRole = "serving"
	RoleRedirect BlockRole = "redirect"
)

// BlockRef references another server { } block. Used to report the blocks that
// collide with a given block (same server_name on the same listen).
type BlockRef struct {
	ServerNames []string `json:"server_names"`
	SourceFile  string   `json:"source_file"`
	SSL         bool     `json:"ssl"`
}

// DomainInfo is the derived state for a single nginx server { } block — the
// block is the subject. SSL cert data is owned by suctl-mod-certbot and joined
// into the view at render time via the broker wire.
type DomainInfo struct {
	Domain       string     `json:"domain"`
	ServerNames  []string   `json:"server_names"`
	WWWMode      WWWMode    `json:"www_mode"`
	Role         BlockRole  `json:"role"`
	RedirectTo   string     `json:"redirect_to,omitempty"`
	Suspended    bool       `json:"suspended"`
	Maintenance  bool       `json:"maintenance"`
	ConfigFile   string     `json:"config_file,omitempty"`
	SSL          bool       `json:"ssl"`
	Conflict     bool       `json:"conflict"`
	ConflictWith []BlockRef `json:"conflict_with,omitempty"`
	Status       Status     `json:"status"`
}

type listenSpec struct {
	port string
	ssl  bool
}

type serverBlock struct {
	serverNames  []string
	listens      []listenSpec
	ssl          bool
	root         string
	redirect     bool
	redirectCode int
	redirectTo   string
	sourceFile   string
}

func sitesAvailablePath(domain string) string { return filepath.Join(sitesAvailableDir, domain+".conf") }
func sitesEnabledPath(domain string) string   { return filepath.Join(sitesEnabledDir, domain+".conf") }
func suspensionConfPath(domain string) string {
	return filepath.Join(confdDir, "00-suctl-suspended-"+domain+".conf")
}
func maintenanceConfPath(domain string) string {
	return filepath.Join(confdDir, "00-suctl-maintenance-"+domain+".conf")
}

func parseNginxTree() ([]serverBlock, error) {
	payload, err := crossplane.Parse(nginxConfRoot, parseOpts())
	if err != nil {
		return nil, fmt.Errorf("nginx parse: %w", err)
	}
	var blocks []serverBlock
	for _, cfg := range payload.Config {
		blocks = append(blocks, blocksFromConfig(cfg)...)
	}
	return blocks, nil
}

// parseListen extracts the port and ssl flag from a listen directive's args,
// handling forms like "80", "443 ssl", "[::]:443 ssl", "0.0.0.0:8080".
func parseListen(args []string) listenSpec {
	ls := listenSpec{}
	if len(args) == 0 {
		return ls
	}
	ls.port = portFromListen(args[0])
	for _, a := range args {
		if a == "ssl" {
			ls.ssl = true
		}
	}
	if ls.port == "443" {
		ls.ssl = true
	}
	return ls
}

func portFromListen(spec string) string {
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		return spec[i+1:]
	}
	return spec
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ports returns the listen ports for a block, defaulting to "80" when the
// block has no explicit listen (nginx's implicit default).
func (b serverBlock) ports() []string {
	if len(b.listens) == 0 {
		return []string{"80"}
	}
	var ps []string
	for _, l := range b.listens {
		if l.port != "" {
			ps = append(ps, l.port)
		}
	}
	if len(ps) == 0 {
		return []string{"80"}
	}
	return ps
}

func isSuctlOverride(b serverBlock) bool { return strings.HasPrefix(b.root, "/var/www/suctl/") }
func domainInBlock(domain string, names []string) bool {
	for _, name := range names {
		if name == domain || name == "www."+domain {
			return true
		}
	}
	return false
}

var ipRe = regexp.MustCompile(`^[0-9a-fA-F:.]+$`)

// blockPrimary picks the row identity for a server block. It prefers a real
// (non-www) hostname; for a www-only or redirect block it falls back to the
// www host so the block is still addressable. Catch-all (_/localhost/IP-only)
// blocks return ok=false and are not shown as rows.
func blockPrimary(names []string) (string, bool) {
	for _, name := range names {
		if name == "_" || name == "localhost" || ipRe.MatchString(name) || strings.HasPrefix(name, "www.") {
			continue
		}
		return name, true
	}
	for _, name := range names {
		if name == "_" || name == "localhost" || ipRe.MatchString(name) {
			continue
		}
		return name, true
	}
	return "", false
}

func shareName(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func contains(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// blocksConflict reports whether two blocks claim the same server_name on the
// same listen port — the only true nginx ambiguity (one wins at runtime).
func blocksConflict(a, b serverBlock) bool {
	if !shareName(a.serverNames, b.serverNames) {
		return false
	}
	for _, pa := range a.ports() {
		for _, pb := range b.ports() {
			if pa == pb {
				return true
			}
		}
	}
	return false
}

// blockToInfo projects a parsed serverBlock into the public DomainInfo subject.
func blockToInfo(b serverBlock, primary string) *DomainInfo {
	m := &DomainInfo{
		Domain:      primary,
		ServerNames: append([]string{}, b.serverNames...),
		ConfigFile:  b.sourceFile,
		SSL:         b.ssl,
		WWWMode:     WWWNone,
		Role:        RoleServing,
	}
	if b.redirect {
		m.Role = RoleRedirect
		m.RedirectTo = b.redirectTo
	}
	hasWWW, hasNonWWW := false, false
	for _, n := range b.serverNames {
		if strings.HasPrefix(n, "www.") {
			hasWWW = true
		} else if n != "_" {
			hasNonWWW = true
		}
	}
	switch {
	case hasWWW && hasNonWWW:
		m.WWWMode = WWWSameBlock
	case hasWWW && m.Role == RoleRedirect:
		m.WWWMode = WWWRedirect
	}
	return m
}

// applyOverrides folds suctl suspension/maintenance conf.d blocks onto a
// subject when they share any server_name with it.
func applyOverrides(m *DomainInfo, overrides []serverBlock) {
	for _, o := range overrides {
		if !shareName(m.ServerNames, o.serverNames) {
			continue
		}
		switch o.root {
		case suctlSuspendedWebroot:
			m.Suspended = true
		case suctlMaintenanceWebroot:
			m.Maintenance = true
		}
	}
}

// hasLiveCert reports whether /etc/letsencrypt/live/{domain}/fullchain.pem
// is a readable, decodable certificate. Used by suspend/maintenance writers
// to decide whether to emit the HTTPS variant of an override block.
func hasLiveCert(domain string) bool {
	certPath := fmt.Sprintf("/etc/letsencrypt/live/%s/fullchain.pem", domain)
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return false
	}
	return true
}

// deriveStatus collapses the override + conflict flags into the public status.
// Conflict supersedes everything else because operator intervention is required.
func deriveStatus(suspended, maintenance, conflict bool) Status {
	if conflict {
		return StatusConflict
	}
	if suspended {
		return StatusSuspended
	}
	if maintenance {
		return StatusMaintenance
	}
	return StatusActive
}

// splitBlocks separates parsed blocks into serving (real or redirect) and
// suctl override blocks.
func splitBlocks(blocks []serverBlock) (serving, overrides []serverBlock) {
	for _, b := range blocks {
		if isSuctlOverride(b) {
			overrides = append(overrides, b)
			continue
		}
		serving = append(serving, b)
	}
	return serving, overrides
}

// ReadDomain derives the DomainInfo for the single server block identified by
// domain (its primary server_name). Status is never stored or cached: the nginx
// config tree is the source of truth; reading live is the only honest way.
func ReadDomain(domain string) (*DomainInfo, error) {
	blocks, err := parseNginxTree()
	if err != nil {
		return nil, err
	}
	serving, overrides := splitBlocks(blocks)
	idx := -1
	for i := range serving {
		if p, ok := blockPrimary(serving[i].serverNames); ok && p == domain {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i := range serving {
			if domainInBlock(domain, serving[i].serverNames) || contains(serving[i].serverNames, domain) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("domain %q not found in nginx config", domain)
	}
	primary, ok := blockPrimary(serving[idx].serverNames)
	if !ok {
		primary = domain
	}
	m := blockToInfo(serving[idx], primary)
	for i := range serving {
		if i == idx {
			continue
		}
		if blocksConflict(serving[idx], serving[i]) {
			m.Conflict = true
			m.ConflictWith = append(m.ConflictWith, BlockRef{
				ServerNames: append([]string{}, serving[i].serverNames...),
				SourceFile:  serving[i].sourceFile,
				SSL:         serving[i].ssl,
			})
		}
	}
	applyOverrides(m, overrides)
	m.Status = deriveStatus(m.Suspended, m.Maintenance, m.Conflict)
	return m, nil
}

// ListDomains discovers every server { } block in the live config tree and
// returns one subject per block. Override (suctl) blocks are folded onto the
// blocks they cover; catch-all/IP-only blocks are skipped. A block is in
// conflict when another block claims the same server_name on the same listen.
func ListDomains() ([]*DomainInfo, error) {
	blocks, err := parseNginxTree()
	if err != nil {
		return nil, err
	}
	serving, overrides := splitBlocks(blocks)
	result := make([]*DomainInfo, 0, len(serving))
	for i := range serving {
		primary, ok := blockPrimary(serving[i].serverNames)
		if !ok {
			continue
		}
		m := blockToInfo(serving[i], primary)
		for j := range serving {
			if i == j {
				continue
			}
			if blocksConflict(serving[i], serving[j]) {
				m.Conflict = true
				m.ConflictWith = append(m.ConflictWith, BlockRef{
					ServerNames: append([]string{}, serving[j].serverNames...),
					SourceFile:  serving[j].sourceFile,
					SSL:         serving[j].ssl,
				})
			}
		}
		applyOverrides(m, overrides)
		m.Status = deriveStatus(m.Suspended, m.Maintenance, m.Conflict)
		result = append(result, m)
	}
	return result, nil
}


func requireNginx() *errorDetail {
	if _, err := exec.LookPath("nginx"); err != nil {
		return &errorDetail{Code: "CALLABLE_FAILED", Message: "nginx binary not found in PATH — install nginx"}
	}
	return nil
}

func requireDomainArg(args map[string]interface{}) (string, bool) {
	// Accept "subject" (standard REPL action param) with fallback to "domain".
	d, _ := args["subject"].(string)
	if d == "" {
		d, _ = args["domain"].(string)
	}
	s := strings.TrimSpace(d)
	return s, s != ""
}

// nginxTest runs `nginx -t` to validate the current configuration.
// Returns a detailed error (including nginx's stderr) on failure.
func nginxTest() error {
	out, err := exec.Command("nginx", "-t").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx config test failed: %v\n%s", err, out)
	}
	return nil
}



// nginxReload signals the running nginx master to reload its configuration
// using `nginx -s reload`. This bypasses systemd entirely, so it works
// regardless of whether systemd's unit cache is stale.
func nginxReload() error {
	// Pre-flight: validate config before touching the running process.
	if err := nginxTest(); err != nil {
		return err
	}
	out, err := exec.Command("nginx", "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx reload signal failed: %v\n%s", err, out)
	}
	return nil
}

// GetServerIPs returns non-loopback IP addresses of this server.
func GetServerIPs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

// resolveDNS classifies how a block's server_names line up with this host's IPs.
// It returns "local" when any name resolves to one of our IPs, "elsewhere" (with
// the first off-host IP seen) when names resolve only to other addresses, or
// "unresolved" when nothing resolved within ctx. Wildcard and IP-form names are
// skipped. The result is advisory: a proxy/LB in front of the host reads as
// "elsewhere" even though traffic still arrives, so callers must not gate
// destructive actions on it.
func resolveDNS(ctx context.Context, names []string) (state, detail string) {
	ours := make(map[string]bool)
	for _, ip := range GetServerIPs() {
		ours[ip.String()] = true
	}
	var resolver net.Resolver
	var firstRemote string
	resolvedAny := false
	for _, name := range names {
		if name == "" || strings.HasPrefix(name, "*") || net.ParseIP(name) != nil {
			continue
		}
		addrs, err := resolver.LookupHost(ctx, name)
		if err != nil || len(addrs) == 0 {
			continue
		}
		resolvedAny = true
		for _, a := range addrs {
			if ours[a] {
				return "local", ""
			}
			if firstRemote == "" {
				firstRemote = a
			}
		}
	}
	if !resolvedAny {
		return "unresolved", ""
	}
	return "elsewhere", firstRemote
}
