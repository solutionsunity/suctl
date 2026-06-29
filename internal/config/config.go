// SPDX-License-Identifier: Apache-2.0

// Package config reads the suctl configuration file (paths.ConfigFile) and
// exposes typed settings.
//
// The file uses YAML format:
//
//	logo_url: https://example.com/logo.png
//	contact_url: https://example.com/contact
//	health_max_restarts: 5
//	module_paths:
//	  - /opt/custom/suctl/modules
//
// Unknown keys are silently ignored so the file can grow over time.
// Load() always returns a valid *Config — missing keys fall back to defaults.
package config

import (
	"os"

	"github.com/solutionsunity/suctl/sdk/paths"
	"gopkg.in/yaml.v3"
)

// ConfigFile is the canonical path of the main suctl configuration file,
// resolved per-OS by sdk/paths (the single source of truth for all suctl paths).
var ConfigFile = paths.ConfigFile

const (
	// DefaultLogoURL is the fallback company logo used in static pages.
	DefaultLogoURL = "https://solutionsunity.com/web/image/website/1/logo"

	// DefaultContactURL is the fallback support link used in static pages.
	DefaultContactURL = "https://solutionsunity.com/contactus"

	// DefaultHealthMaxRestarts is the number of times the lifecycle orchestrator
	// restarts an unhealthy core-managed module before marking it failed.
	DefaultHealthMaxRestarts = 5

	// The following are the compiled-in fallbacks for the core-internal
	// timeouts and limits consolidated into suctl.conf (Gate D). Each is
	// expressed in whole seconds (or a raw count) and is deliberately generous;
	// an operator only overrides them to tune slow or constrained hosts. SDK
	// timeouts (protocol/brokerclient/conformance) live in the SDK module and
	// are not routed here.

	// DefaultHandshakeTimeoutSeconds is how long core waits for a module to
	// accept the first connection and respond to handshake after process start.
	DefaultHandshakeTimeoutSeconds = 15

	// DefaultHealthCheckIntervalSeconds is how often core health-checks each
	// active module process.
	DefaultHealthCheckIntervalSeconds = 30

	// DefaultHealthFailThreshold is the number of consecutive failed health
	// checks before the failure escalation fires.
	DefaultHealthFailThreshold = 3

	// DefaultHookTimeoutSeconds is the fallback per-hook deadline when a hook
	// declaration does not set its own timeout_seconds.
	DefaultHookTimeoutSeconds = 30

	// DefaultSupervisorMaxRestarts is the crash-loop guard: a module that
	// crashes more than this many times within the restart window is hard-
	// stopped. Distinct from HealthMaxRestarts (health escalation).
	DefaultSupervisorMaxRestarts = 3

	// DefaultSupervisorRestartWindowSeconds is the sliding window over which the
	// crash-loop guard counts crashes.
	DefaultSupervisorRestartWindowSeconds = 60

	// DefaultSupervisorStopTimeoutSeconds is how long the supervisor waits for a
	// module to exit after SIGTERM before escalating to SIGKILL.
	DefaultSupervisorStopTimeoutSeconds = 30

	// DefaultAdmitTimeoutSeconds backstops gate admission so a wedged
	// reservation can never block an invoke forever. Generous on purpose —
	// it only fires on a bug.
	DefaultAdmitTimeoutSeconds = 300
)

// Config holds the parsed contents of suctl.conf.
// New fields should be added here as suctl grows.
type Config struct {
	// LogoURL is the full URL of the company logo image embedded in the
	// suspension and maintenance HTML pages.
	LogoURL string `yaml:"logo_url"`

	// ContactURL is the full URL of the support / contact page linked from
	// the suspension and maintenance HTML pages.
	ContactURL string `yaml:"contact_url"`

	// ModulePaths is the operator-declared list of additional directories
	// to scan for module installations, appended after the two system paths.
	ModulePaths []string `yaml:"module_paths"`

	// HealthMaxRestarts is how many times the lifecycle orchestrator restarts
	// an unhealthy core-managed module before marking it failed.
	HealthMaxRestarts int `yaml:"health_max_restarts"`

	// The following are core-internal timeouts/limits (Gate D). Each is in
	// whole seconds (or a raw count); a missing or non-positive value falls
	// back to its compiled-in default.

	// HandshakeTimeoutSeconds caps the wait for a module's first handshake.
	HandshakeTimeoutSeconds int `yaml:"handshake_timeout_seconds"`

	// HealthCheckIntervalSeconds is the per-module health-check period.
	HealthCheckIntervalSeconds int `yaml:"health_check_interval_seconds"`

	// HealthFailThreshold is the consecutive-failure count that escalates.
	HealthFailThreshold int `yaml:"health_fail_threshold"`

	// HookTimeoutSeconds is the fallback per-hook deadline.
	HookTimeoutSeconds int `yaml:"hook_timeout_seconds"`

	// SupervisorMaxRestarts is the crash-loop guard threshold.
	SupervisorMaxRestarts int `yaml:"supervisor_max_restarts"`

	// SupervisorRestartWindowSeconds is the crash-loop guard window.
	SupervisorRestartWindowSeconds int `yaml:"supervisor_restart_window_seconds"`

	// SupervisorStopTimeoutSeconds is the SIGTERM→SIGKILL grace period.
	SupervisorStopTimeoutSeconds int `yaml:"supervisor_stop_timeout_seconds"`

	// AdmitTimeoutSeconds backstops gate admission.
	AdmitTimeoutSeconds int `yaml:"admit_timeout_seconds"`
}

// Load reads ConfigFile and returns a *Config populated with its values.
// If the file does not exist or cannot be read, all fields fall back to
// their compiled-in defaults — Load never returns nil and never errors.
func Load() *Config {
	return LoadFrom(ConfigFile)
}

// Default returns a *Config with every field at its compiled-in fallback,
// reading no file. Intended for callers that need a fully-populated config
// without touching disk (e.g. nil-safety in startup).
func Default() *Config {
	return defaults()
}

// LoadFrom reads the given path instead of ConfigFile.
// Intended for tests and bootstrap's install path.
func LoadFrom(path string) *Config {
	c := defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	apply(c, data)
	return c
}

// defaults returns a Config with all compiled-in fallback values.
func defaults() *Config {
	return &Config{
		LogoURL:                        DefaultLogoURL,
		ContactURL:                     DefaultContactURL,
		HealthMaxRestarts:              DefaultHealthMaxRestarts,
		HandshakeTimeoutSeconds:        DefaultHandshakeTimeoutSeconds,
		HealthCheckIntervalSeconds:     DefaultHealthCheckIntervalSeconds,
		HealthFailThreshold:            DefaultHealthFailThreshold,
		HookTimeoutSeconds:             DefaultHookTimeoutSeconds,
		SupervisorMaxRestarts:          DefaultSupervisorMaxRestarts,
		SupervisorRestartWindowSeconds: DefaultSupervisorRestartWindowSeconds,
		SupervisorStopTimeoutSeconds:   DefaultSupervisorStopTimeoutSeconds,
		AdmitTimeoutSeconds:            DefaultAdmitTimeoutSeconds,
	}
}

// apply unmarshals YAML data into c, overwriting only the fields present in
// the file. Fields absent from the file keep their defaults() values.
func apply(c *Config, data []byte) {
	// yaml.Unmarshal does not zero fields not present in the document when
	// called on a pre-populated struct — absent keys are left at their
	// current (default) values.
	_ = yaml.Unmarshal(data, c)
	// Restore defaults for zero-valued string fields so callers always get
	// a non-empty LogoURL and ContactURL.
	if c.LogoURL == "" {
		c.LogoURL = DefaultLogoURL
	}
	if c.ContactURL == "" {
		c.ContactURL = DefaultContactURL
	}
	// A missing or non-positive value falls back to the default rather than
	// disabling restarts entirely.
	if c.HealthMaxRestarts <= 0 {
		c.HealthMaxRestarts = DefaultHealthMaxRestarts
	}
	// Each core-internal timeout/limit falls back to its compiled-in default
	// when absent or non-positive — operators cannot accidentally set a zero
	// (which would mean "no wait" / "no retries") by omitting a key.
	if c.HandshakeTimeoutSeconds <= 0 {
		c.HandshakeTimeoutSeconds = DefaultHandshakeTimeoutSeconds
	}
	if c.HealthCheckIntervalSeconds <= 0 {
		c.HealthCheckIntervalSeconds = DefaultHealthCheckIntervalSeconds
	}
	if c.HealthFailThreshold <= 0 {
		c.HealthFailThreshold = DefaultHealthFailThreshold
	}
	if c.HookTimeoutSeconds <= 0 {
		c.HookTimeoutSeconds = DefaultHookTimeoutSeconds
	}
	if c.SupervisorMaxRestarts <= 0 {
		c.SupervisorMaxRestarts = DefaultSupervisorMaxRestarts
	}
	if c.SupervisorRestartWindowSeconds <= 0 {
		c.SupervisorRestartWindowSeconds = DefaultSupervisorRestartWindowSeconds
	}
	if c.SupervisorStopTimeoutSeconds <= 0 {
		c.SupervisorStopTimeoutSeconds = DefaultSupervisorStopTimeoutSeconds
	}
	if c.AdmitTimeoutSeconds <= 0 {
		c.AdmitTimeoutSeconds = DefaultAdmitTimeoutSeconds
	}
}
