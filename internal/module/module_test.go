// SPDX-License-Identifier: Apache-2.0

package module

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/solutionsunity/suctl/internal/wire"
	"github.com/solutionsunity/suctl/sdk/manifest"
)

// mustParseManifest decodes a manifest JSON string for use in synthetic
// store fixtures. Test-only.
func mustParseManifest(t *testing.T, content string) *manifest.Manifest {
	t.Helper()
	var m manifest.Manifest
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}
	return &m
}

// storeWith builds a Store from the given records. Test-only.
func storeWith(records map[string]*Record) *Store {
	s := NewStore()
	for name, r := range records {
		s.Put(name, r)
	}
	return s
}

// minimalManifest returns a valid manifest JSON for a module with the given name.
func minimalManifest(name string) string {
	return `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "Test module",
  "entrypoint": "` + name + `",
  "requires": {"binaries":[],"paths":[],"sockets":[],"permissions":[],"capabilities":[],"config":[]},
  "capabilities": []
}`
}

// manifestWithPlatform returns a valid manifest JSON whose platform list is the
// single given OS — used to exercise the platform gate.
func manifestWithPlatform(name, platform string) string {
	return `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["` + platform + `"],
  "author": "test",
  "license": "MIT",
  "description": "Test module",
  "entrypoint": "` + name + `",
  "requires": {"binaries":[],"paths":[],"sockets":[],"permissions":[],"capabilities":[],"config":[]},
  "capabilities": []
}`
}

// makeModuleDir creates a module sub-directory with manifest.json under root.
// It also writes a stub entrypoint binary (named after the module, matching the
// manifest's entrypoint) so the dir represents a built module that satisfies the
// scan entrypoint gate.
func makeModuleDir(t *testing.T, root, moduleName string) string {
	t.Helper()
	dir := filepath.Join(root, moduleName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(minimalManifest(moduleName)), 0644); err != nil {
		t.Fatal(err)
	}
	writeEntrypointStub(t, dir, moduleName)
	return dir
}

// writeEntrypointStub writes an empty, executable file named prog into dir so a
// module's declared entrypoint resolves during scan. Existence is all the
// entrypoint gate checks; the file is never executed in unit tests.
func writeEntrypointStub(t *testing.T, dir, prog string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, prog), []byte{}, 0755); err != nil {
		t.Fatal(err)
	}
}

// --------------------------------------------------------------------------
// buildScanOrder
// --------------------------------------------------------------------------

func TestBuildScanOrder_SystemFirst(t *testing.T) {
	order := buildScanOrder([]string{"/extra"})
	if order[0] != SystemModulePath {
		t.Errorf("first = %q; want SystemModulePath", order[0])
	}
	if order[1] != DefaultThirdPartyPath {
		t.Errorf("second = %q; want DefaultThirdPartyPath", order[1])
	}
	if order[2] != "/extra" {
		t.Errorf("third = %q; want /extra", order[2])
	}
}

func TestBuildScanOrder_DeduplicatesExtraPath(t *testing.T) {
	order := buildScanOrder([]string{SystemModulePath, "/extra"})
	for i, p := range order {
		for j, q := range order {
			if i != j && p == q {
				t.Errorf("duplicate path %q at positions %d and %d", p, i, j)
			}
		}
	}
}

func TestBuildScanOrder_BlankExtraSkipped(t *testing.T) {
	order := buildScanOrder([]string{"", "  ", "/extra"})
	for _, p := range order {
		if strings.TrimSpace(p) == "" {
			t.Errorf("blank path included in scan order")
		}
	}
}

// --------------------------------------------------------------------------
// Scan — happy paths
// --------------------------------------------------------------------------

func TestScan_EmptyPath_ReturnsEmptyIndex(t *testing.T) {
	root := t.TempDir()
	idx, warns, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Len() != 0 {
		t.Errorf("index len = %d; want 0", idx.Len())
	}
	_ = warns
}

func TestScan_NonexistentPath_Skipped(t *testing.T) {
	_, _, err := Scan([]string{"/this/path/does/not/exist/9999"})
	if err != nil {
		t.Errorf("nonexistent path should be silently skipped, got: %v", err)
	}
}

func TestScan_OneModule(t *testing.T) {
	root := t.TempDir()
	makeModuleDir(t, root, "suctl-mod-nginx")

	idx, warns, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if idx.Len() != 1 {
		t.Fatalf("index len = %d; want 1", idx.Len())
	}
	entry, ok := idx.Get("nginx")
	if !ok {
		t.Fatal("expected key 'nginx' in index")
	}
	if got := manifest.ShortNameFromDir(entry.Dir); got != "nginx" {
		t.Errorf("short name = %q; want nginx", got)
	}
	if entry.State() != StateReady {
		t.Errorf("State = %q; want ready", entry.State())
	}
}

func TestScan_MultipleModules(t *testing.T) {
	root := t.TempDir()
	makeModuleDir(t, root, "suctl-mod-nginx")
	makeModuleDir(t, root, "suctl-mod-fail2ban")

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Len() != 2 {
		t.Errorf("index len = %d; want 2", idx.Len())
	}
	if _, ok := idx.Get("nginx"); !ok {
		t.Error("missing 'nginx' in index")
	}
	if _, ok := idx.Get("fail2ban"); !ok {
		t.Error("missing 'fail2ban' in index")
	}
}

func TestScan_InvalidManifest_Warning(t *testing.T) {
	root := t.TempDir()
	// Create a directory with a broken manifest.
	bad := filepath.Join(root, "suctl-mod-broken")
	os.MkdirAll(bad, 0755)
	os.WriteFile(filepath.Join(bad, "manifest.json"), []byte("{bad}"), 0644)

	idx, warns, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Len() != 0 {
		t.Errorf("broken module should not appear in index")
	}
	if len(warns) == 0 {
		t.Error("expected warning for broken manifest")
	}
}

// --------------------------------------------------------------------------
// Scan — platform gate
// --------------------------------------------------------------------------

// TestScan_WrongPlatform_StateUnavailable verifies that a module whose platform
// list does not include the host OS is indexed (visible) as StateUnavailable
// with a reason naming both the host and the module's declared platforms —
// rather than being silently skipped.
func TestScan_WrongPlatform_StateUnavailable(t *testing.T) {
	foreign := "windows"
	if runtime.GOOS == "windows" {
		foreign = "linux"
	}
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-foreign",
		manifestWithPlatform("suctl-mod-foreign", foreign))

	idx, warns, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	entry, ok := idx.Get("foreign")
	if !ok {
		t.Fatal("wrong-OS module should be indexed (visible), not silently skipped")
	}
	if entry.State() != StateUnavailable {
		t.Errorf("State = %q; want %q", entry.State(), StateUnavailable)
	}
	if !strings.Contains(entry.Reason(), runtime.GOOS) || !strings.Contains(entry.Reason(), foreign) {
		t.Errorf("Reason should name host %q and module platform %q; got %q",
			runtime.GOOS, foreign, entry.Reason())
	}
}

// TestScan_HostPlatform_StateReady is the positive companion: a module that
// lists the host OS is indexed StateReady with no reason.
func TestScan_HostPlatform_StateReady(t *testing.T) {
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-native",
		manifestWithPlatform("suctl-mod-native", runtime.GOOS))

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := idx.Get("native")
	if !ok {
		t.Fatal("host-OS module should be indexed")
	}
	if entry.State() != StateReady {
		t.Errorf("State = %q; want %q", entry.State(), StateReady)
	}
	if entry.Reason() != "" {
		t.Errorf("Reason should be empty for a ready module; got %q", entry.Reason())
	}
}

// --------------------------------------------------------------------------
// Scan — entrypoint gate
// --------------------------------------------------------------------------

// TestScan_MissingEntrypoint_StateUnavailable verifies the entrypoint gate: a
// directory with a valid, host-platform manifest but no built entrypoint binary
// (and whose entrypoint is not a PATH command) is indexed visible-but-
// StateUnavailable rather than advertised StateReady. This mirrors a manifest-
// only placeholder module whose binary has not been built/installed.
func TestScan_MissingEntrypoint_StateUnavailable(t *testing.T) {
	root := t.TempDir()
	// makeModuleDirWithManifest writes a stub binary; create the dir manually so
	// only manifest.json is present (no entrypoint file).
	dir := filepath.Join(root, "suctl-mod-stub")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(minimalManifest("suctl-mod-stub")), 0644); err != nil {
		t.Fatal(err)
	}

	idx, warns, err := scanPaths([]string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	entry, ok := idx.Get("stub")
	if !ok {
		t.Fatal("binary-less module should still be indexed (visible)")
	}
	if entry.State() != StateUnavailable {
		t.Errorf("State = %q; want %q", entry.State(), StateUnavailable)
	}
	if !strings.Contains(entry.Reason(), "entrypoint") {
		t.Errorf("Reason = %q; want mention of entrypoint", entry.Reason())
	}
}

// TestEntrypointResolvable covers the three resolution paths the supervisor
// uses at launch: a file inside the module dir, an absolute existing path, and a
// command on PATH; plus the negative case.
func TestEntrypointResolvable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "suctl-mod-x"), []byte{}, 0755)

	ep := func(s string) manifest.Entrypoint { return manifest.Entrypoint{Parts: []string{s}} }

	if !entrypointResolvable(ep("suctl-mod-x"), dir) {
		t.Error("file in module dir should resolve")
	}
	abs := filepath.Join(dir, "suctl-mod-x")
	if !entrypointResolvable(ep(abs), dir) {
		t.Error("existing absolute path should resolve")
	}
	if entrypointResolvable(ep("suctl-mod-x"), t.TempDir()) {
		t.Error("absent file (no dir match, not on PATH) should not resolve")
	}
	if entrypointResolvable(ep("__suctl_no_such_cmd_99__"), dir) {
		t.Error("nonexistent PATH command should not resolve")
	}
	// PATH path: pick the first command that actually exists on this host so the
	// assertion is deterministic regardless of the test environment.
	for _, cand := range []string{"sh", "ls", "cat", "env", "go"} {
		if _, err := exec.LookPath(cand); err == nil {
			if !entrypointResolvable(ep(cand), t.TempDir()) {
				t.Errorf("PATH command %q should resolve", cand)
			}
			break
		}
	}
}

// --------------------------------------------------------------------------
// Scan — conflict detection
// --------------------------------------------------------------------------

// TestScan_ConflictSameNameTwoPaths verifies that a short-name collision is
// non-fatal: Scan returns no error, the module is in the index as
// StateUnavailable, and the warning message mentions both paths.
func TestScan_ConflictSameNameTwoPaths(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	makeModuleDir(t, rootA, "suctl-mod-nginx")
	makeModuleDir(t, rootB, "suctl-mod-nginx")

	idx, warns, err := scanPaths([]string{rootA, rootB})
	if err != nil {
		t.Fatalf("Scan must not return error on conflict, got: %v", err)
	}
	entry, ok := idx.Get("nginx")
	if !ok {
		t.Fatal("conflicting module should still appear in index")
	}
	if entry.State() != StateUnavailable {
		t.Errorf("conflicting module state = %q; want %q", entry.State(), StateUnavailable)
	}
	if entry.Reason() == "" {
		t.Error("conflicting module Reason should be set")
	}
	if len(warns) == 0 {
		t.Error("expected at least one conflict warning")
	}
	warnText := strings.Join(warns, " ")
	if !strings.Contains(warnText, rootA) || !strings.Contains(warnText, rootB) {
		t.Errorf("conflict warning should mention both paths; got: %q", warnText)
	}
}

func TestScan_ConflictError_MessageFormat(t *testing.T) {
	ce := &ConflictError{ShortName: "nginx", PathA: "/a/b", PathB: "/c/d"}
	msg := ce.Error()
	if !strings.Contains(msg, "nginx") {
		t.Errorf("error missing module name: %q", msg)
	}
	if !strings.Contains(msg, "/a/b") || !strings.Contains(msg, "/c/d") {
		t.Errorf("error missing paths: %q", msg)
	}
}

// --------------------------------------------------------------------------
// Scan — path ordering (system path first)
// --------------------------------------------------------------------------

func TestScan_PathOrder_SystemWins(t *testing.T) {
	// Two paths: first has suctl-mod-nginx, second has a different module.
	// The key is that the first path's module appears in the index.
	pathA := t.TempDir()
	pathB := t.TempDir()
	makeModuleDir(t, pathA, "suctl-mod-nginx")
	makeModuleDir(t, pathB, "suctl-mod-fail2ban")

	idx, _, err := scanPaths([]string{pathA, pathB})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := idx.Get("nginx"); !ok || e.Dir != filepath.Join(pathA, "suctl-mod-nginx") {
		t.Error("nginx entry should come from pathA")
	}
	if _, ok := idx.Get("fail2ban"); !ok {
		t.Error("fail2ban entry should come from pathB")
	}
}

// --------------------------------------------------------------------------
// Entry default state
// --------------------------------------------------------------------------

func TestScan_EntryDefaultState(t *testing.T) {
	root := t.TempDir()
	makeModuleDir(t, root, "suctl-mod-certbot")

	idx, _, _ := scanPaths([]string{root})
	certbot, _ := idx.Get("certbot")
	if certbot.State() != StateReady {
		t.Errorf("new entry state = %q; want ready", certbot.State())
	}
	if certbot.Reason() != "" {
		t.Errorf("new entry reason = %q; want empty", certbot.Reason())
	}
}

// --------------------------------------------------------------------------
// State constants
// --------------------------------------------------------------------------

func TestStateConstants(t *testing.T) {
	if StateReady != "ready" {
		t.Errorf("StateReady = %q; want ready", StateReady)
	}
	if StateActive != "active" {
		t.Errorf("StateActive = %q; want active", StateActive)
	}
	if StateUnavailable != "unavailable" {
		t.Errorf("StateUnavailable = %q; want unavailable", StateUnavailable)
	}
	if StateMissing != "missing" {
		t.Errorf("StateMissing = %q; want missing", StateMissing)
	}
}

// --------------------------------------------------------------------------
// Helpers for two-pass tests
// --------------------------------------------------------------------------

// manifestWithCapabilities returns a minimal manifest JSON that declares
// the given capabilities and requires the given capability names.
func manifestWithCapabilities(moduleName string, capNames []string, requiresCaps []string) string {
	caps := ""
	for i, n := range capNames {
		if i > 0 {
			caps += ","
		}
		caps += `{"name":"` + n + `","description":"test","async":false,"params":[]}`
	}
	reqs := ""
	for i, r := range requiresCaps {
		if i > 0 {
			reqs += ","
		}
		reqs += `"` + r + `"`
	}
	return `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "test",
  "entrypoint": "` + moduleName + `",
  "requires": {"binaries":[],"paths":[],"sockets":[],"permissions":[],"capabilities":[` + reqs + `],"config":[]},
  "capabilities": [` + caps + `]
}`
}

func makeModuleDirWithManifest(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(content), 0644)
	writeEntrypointStub(t, dir, name)
	return dir
}

// --------------------------------------------------------------------------
// PendingSurface
// --------------------------------------------------------------------------

func TestPendingSurface_EmptyIndex(t *testing.T) {
	surface := PendingSurface(NewStore())
	if len(surface) != 0 {
		t.Errorf("surface len = %d; want 0", len(surface))
	}
}

func TestPendingSurface_CollectsAllCapabilities(t *testing.T) {
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-nginx",
		manifestWithCapabilities("suctl-mod-nginx",
			[]string{"nginx.domain.create", "nginx.reload"}, nil))
	makeModuleDirWithManifest(t, root, "suctl-mod-certbot",
		manifestWithCapabilities("suctl-mod-certbot",
			[]string{"certbot.ssl.provision"}, nil))

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	surface := PendingSurface(idx)
	for _, want := range []string{"nginx.domain.create", "nginx.reload", "certbot.ssl.provision"} {
		if !surface[want] {
			t.Errorf("surface missing %q", want)
		}
	}
}

func TestPendingSurface_ModuleWithNoCapabilities(t *testing.T) {
	root := t.TempDir()
	makeModuleDir(t, root, "suctl-mod-basic")

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	surface := PendingSurface(idx)
	if len(surface) != 0 {
		t.Errorf("surface len = %d; want 0 for module with no capabilities", len(surface))
	}
}

// --------------------------------------------------------------------------
// EvaluateRequirements
// --------------------------------------------------------------------------

func TestEvaluateRequirements_CapabilityMet(t *testing.T) {
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-nginx",
		manifestWithCapabilities("suctl-mod-nginx", []string{"nginx.domain.create"}, nil))
	makeModuleDirWithManifest(t, root, "suctl-mod-certbot",
		manifestWithCapabilities("suctl-mod-certbot",
			[]string{"certbot.ssl.provision"}, []string{"nginx.domain.create"}))

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx)

	// certbot requires nginx.domain.create — nginx is ready so the cap is in ReadySurface.
	certbot, _ := idx.Get("certbot")
	if certbot.State() != StateReady {
		t.Errorf("certbot state = %q; want ready (capability met)", certbot.State())
	}
}

func TestEvaluateRequirements_CapabilityMissing(t *testing.T) {
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-certbot",
		manifestWithCapabilities("suctl-mod-certbot",
			[]string{"certbot.ssl.provision"}, []string{"nginx.domain.create"}))

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx) // nginx not in index → nginx.domain.create not in ReadySurface

	e, _ := idx.Get("certbot")
	if e.State() != StateUnavailable {
		t.Errorf("certbot state = %q; want unavailable", e.State())
	}
	if e.Reason() == "" {
		t.Error("expected non-empty Reason for unavailable state")
	}
	if !strings.Contains(e.Reason(), "nginx.domain.create") {
		t.Errorf("Reason should mention missing capability: %q", e.Reason())
	}
}

// TestEvaluateRequirements_CapabilityCascade verifies that when a provider
// module is unavailable (binary missing), dependent modules also become
// unavailable and their reason names the provider.
func TestEvaluateRequirements_CapabilityCascade(t *testing.T) {
	root := t.TempDir()
	// certbot provides certbot.cert.provision but requires a missing binary.
	certbotManifest := `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "certbot util module",
  "entrypoint": "suctl-mod-certbot",
  "requires": {"binaries":["__nonexistent_certbot_suctl_99__"],"paths":[],"sockets":[],"permissions":[],"capabilities":[],"config":[]},
  "capabilities": [{"name":"certbot.cert.provision","description":"provision cert","async":false,"params":[]}]
}`
	// nginx requires certbot.cert.provision.
	nginxManifest := `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "nginx module",
  "entrypoint": "suctl-mod-nginx",
  "requires": {"binaries":[],"paths":[],"sockets":[],"permissions":[],"capabilities":["certbot.cert.provision"],"config":[]},
  "capabilities": []
}`
	makeModuleDirWithManifest(t, root, "suctl-mod-certbot", certbotManifest)
	makeModuleDirWithManifest(t, root, "suctl-mod-nginx", nginxManifest)

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx)

	// certbot itself must be unavailable (binary missing).
	certbot, _ := idx.Get("certbot")
	if certbot.State() != StateUnavailable {
		t.Errorf("certbot state = %q; want unavailable (binary missing)", certbot.State())
	}

	// nginx must cascade to unavailable because certbot is unavailable.
	nginx, _ := idx.Get("nginx")
	if nginx.State() != StateUnavailable {
		t.Errorf("nginx state = %q; want unavailable (cascade from certbot)", nginx.State())
	}
	if !strings.Contains(nginx.Reason(), "certbot") {
		t.Errorf("nginx Reason should name provider certbot: %q", nginx.Reason())
	}
	if !strings.Contains(nginx.Reason(), "certbot.cert.provision") {
		t.Errorf("nginx Reason should mention the capability: %q", nginx.Reason())
	}
}

func TestEvaluateRequirements_MissingBinary(t *testing.T) {
	root := t.TempDir()
	// Write a manifest that requires a binary that almost certainly does not exist.
	content := `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "test",
  "entrypoint": "suctl-mod-test",
  "requires": {"binaries":["__nonexistent_binary_suctl_99__"],"paths":[],"sockets":[],"permissions":[],"capabilities":[],"config":[]},
  "capabilities": []
}`
	makeModuleDirWithManifest(t, root, "suctl-mod-test", content)

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx)

	e, _ := idx.Get("test")
	if e.State() != StateUnavailable {
		t.Errorf("state = %q; want unavailable for missing binary", e.State())
	}
	if !strings.Contains(e.Reason(), "__nonexistent_binary_suctl_99__") {
		t.Errorf("Reason should mention missing binary: %q", e.Reason())
	}
}

func TestEvaluateRequirements_MissingPath(t *testing.T) {
	root := t.TempDir()
	content := `{
  "version": "0.1.0",
  "protocol": "1",
  "platform": ["linux"],
  "author": "test",
  "license": "MIT",
  "description": "test",
  "entrypoint": "suctl-mod-test",
  "requires": {"binaries":[],"paths":["/nonexistent/path/suctl/test/9999"],"sockets":[],"permissions":[],"capabilities":[],"config":[]},
  "capabilities": []
}`
	makeModuleDirWithManifest(t, root, "suctl-mod-test", content)

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx)

	e, _ := idx.Get("test")
	if e.State() != StateUnavailable {
		t.Errorf("state = %q; want unavailable for missing path", e.State())
	}
}

func TestEvaluateRequirements_NoRequirements_StaysReady(t *testing.T) {
	root := t.TempDir()
	makeModuleDir(t, root, "suctl-mod-nginx")

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	EvaluateRequirements(idx)

	nginx, _ := idx.Get("nginx")
	if nginx.State() != StateReady {
		t.Errorf("state = %q; want ready when no requirements", nginx.State())
	}
}

func TestReadySurface_ExcludesUnavailable(t *testing.T) {
	root := t.TempDir()
	makeModuleDirWithManifest(t, root, "suctl-mod-nginx",
		manifestWithCapabilities("suctl-mod-nginx", []string{"nginx.domain.create"}, nil))
	makeModuleDirWithManifest(t, root, "suctl-mod-certbot",
		manifestWithCapabilities("suctl-mod-certbot", []string{"certbot.ssl.provision"}, nil))

	idx, _, err := scanPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	// Manually mark certbot unavailable (simulating binary missing).
	certbot, _ := idx.Get("certbot")
	certbot.SetStatus(StateUnavailable, "")

	surface := ReadySurface(idx)

	if surface["certbot.ssl.provision"] {
		t.Error("ReadySurface should exclude capabilities from unavailable modules")
	}
	if !surface["nginx.domain.create"] {
		t.Error("ReadySurface should include capabilities from ready modules")
	}
}

// --------------------------------------------------------------------------
// RequiredInactiveProviders
// --------------------------------------------------------------------------

func TestRequiredInactiveProviders_NoRequirements(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", []string{"nginx.domain.create"}, nil)),
		},
	})
	got := RequiredInactiveProviders("nginx", idx)
	if len(got) != 0 {
		t.Errorf("got %v; want empty when no requires.capabilities", got)
	}
}

func TestRequiredInactiveProviders_ProviderInactive(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", nil, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := RequiredInactiveProviders("nginx", idx)
	if len(got) != 1 || got[0] != "certbot" {
		t.Errorf("got %v; want [certbot]", got)
	}
}

func TestRequiredInactiveProviders_ProviderAlreadyActive(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", nil, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := RequiredInactiveProviders("nginx", idx)
	if len(got) != 0 {
		t.Errorf("got %v; want empty when provider already active", got)
	}
}

func TestRequiredInactiveProviders_TransitiveChain(t *testing.T) {
	// webapps → nginx → certbot, all ready, none active.
	// Expect depth-first order: certbot before nginx.
	idx := storeWith(map[string]*Record{
		"webapps": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-webapps", nil, []string{"nginx.domain.create"})),
		},
		"nginx": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", []string{"nginx.domain.create"}, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateReady,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := RequiredInactiveProviders("webapps", idx)
	want := []string{"certbot", "nginx"}
	if len(got) != len(want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q; want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// --------------------------------------------------------------------------
// Footprint (reservation set)
// --------------------------------------------------------------------------

func TestFootprint_SelfOnly(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", []string{"nginx.domain.create"}, nil)),
		},
	})
	got := Footprint("nginx", idx)
	if len(got) != 1 || !got["nginx"] {
		t.Errorf("got %v; want {nginx} for a module with no requires", got)
	}
}

func TestFootprint_SingleProvider(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", nil, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := Footprint("nginx", idx)
	if len(got) != 2 || !got["nginx"] || !got["certbot"] {
		t.Errorf("got %v; want {nginx, certbot}", got)
	}
}

func TestFootprint_TransitiveChain(t *testing.T) {
	// webapps → nginx → certbot. Footprint of webapps reserves all three.
	idx := storeWith(map[string]*Record{
		"webapps": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-webapps", nil, []string{"nginx.domain.create"})),
		},
		"nginx": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", []string{"nginx.domain.create"}, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := Footprint("webapps", idx)
	if len(got) != 3 || !got["webapps"] || !got["nginx"] || !got["certbot"] {
		t.Errorf("got %v; want {webapps, nginx, certbot}", got)
	}
}

// TestFootprint_Cycle proves cycle-tolerance: a ⇄ b mutually require each
// other's capability. Footprint must terminate and reserve both.
func TestFootprint_Cycle(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"a": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-a", []string{"a.do"}, []string{"b.do"})),
		},
		"b": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-b", []string{"b.do"}, []string{"a.do"})),
		},
	})
	got := Footprint("a", idx)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Errorf("got %v; want {a, b} (cycle must terminate)", got)
	}
}

// TestFootprint_IncludesInactiveProvider proves Footprint is state-agnostic:
// a provider that is StateUnavailable still appears (it is part of the static
// reservation set), unlike RequiredInactiveProviders which filters by state.
func TestFootprint_IncludesInactiveProvider(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", nil, []string{"certbot.cert.provision"})),
		},
		"certbot": {
			state: StateUnavailable,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-certbot", []string{"certbot.cert.provision"}, nil)),
		},
	})
	got := Footprint("nginx", idx)
	if !got["certbot"] {
		t.Errorf("got %v; want certbot present despite StateUnavailable", got)
	}
}

func TestFootprint_MissingProvider(t *testing.T) {
	idx := storeWith(map[string]*Record{
		"nginx": {
			state: StateActive,
			Manifest: mustParseManifest(t, manifestWithCapabilities(
				"suctl-mod-nginx", nil, []string{"nobody.provides.this"})),
		},
	})
	got := Footprint("nginx", idx)
	if len(got) != 1 || !got["nginx"] {
		t.Errorf("got %v; want {nginx} when the required capability has no provider", got)
	}
}

// --------------------------------------------------------------------------
// IsOutOfSync
// --------------------------------------------------------------------------

func TestIsOutOfSync(t *testing.T) {
	activationDir := t.TempDir()

	// A record is "running" only when it is active and holds a live wire.
	// hasMux stages that wire mux so the active+wire conjunction is exercised
	// independently of the on-disk flag.
	tests := []struct {
		name          string
		state         State
		hasFlag       bool
		hasMux        bool
		wantOutOfSync bool
	}{
		{"synced_active", StateActive, true, true, false},
		{"synced_ready", StateReady, false, false, false},
		{"not_running", StateActive, true, false, true},
		{"extra_running", StateActive, false, true, true},
		{"should_be_active", StateReady, true, false, true},
		{"skipped_unavailable", StateUnavailable, true, true, false},
		{"skipped_missing", StateMissing, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup flag file
			flagPath := filepath.Join(activationDir, "testmod.flag")
			_ = os.Remove(flagPath)
			if tt.hasFlag {
				_ = os.WriteFile(flagPath, nil, 0644)
			}

			r := &Record{state: tt.state}
			if tt.hasMux {
				c1, c2 := net.Pipe()
				defer c1.Close()
				defer c2.Close()
				r.SetMux(wire.New(c1, nil))
			}
			idx := storeWith(map[string]*Record{"testmod": r})

			got := IsOutOfSync(idx, activationDir)
			if got != tt.wantOutOfSync {
				t.Errorf("IsOutOfSync() = %v; want %v", got, tt.wantOutOfSync)
			}
		})
	}
}
