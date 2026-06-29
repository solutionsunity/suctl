// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ── Cert read/list ───────────────────────────────────────────────────────────

func cmdCertList(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	certs, err := ListCerts()
	if err != nil {
		return failResult("CALLABLE_FAILED", "read certs: "+err.Error())
	}
	return okResult(map[string]interface{}{"certificates": certs})
}

func cmdCertRead(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	ci := ReadCert(name)
	if ci.Status == StatusMissing && ci.NotAfter == "" {
		return failResult("CALLABLE_FAILED", fmt.Sprintf("certificate %q not found", name))
	}
	return okResult(ci)
}

// ── Cert lifecycle via certbot CLI ───────────────────────────────────────────

func cmdCertProvision(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireCertbot(); ed != nil {
		return nil, ed
	}
	domain, _ := args["domain"].(string)
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return failResult("INVALID_PARAMS", "domain is required")
	}
	email, _ := args["email"].(string)
	email = strings.TrimSpace(email)
	if email == "" {
		return failResult("INVALID_PARAMS", "email is required")
	}
	cArgs := []string{"--nginx", "-d", domain, "--non-interactive", "--agree-tos", "--email", email}
	if extra, _ := args["extra_domains"].(string); strings.TrimSpace(extra) != "" {
		for _, d := range strings.Fields(extra) {
			cArgs = append(cArgs, "-d", d)
		}
	}
	out, err := exec.Command("certbot", cArgs...).CombinedOutput()
	if err != nil {
		return failResult("CALLABLE_FAILED", "certbot: "+err.Error()+"\n"+string(out))
	}
	return okResult(map[string]interface{}{"name": domain, "provisioned": true, "output": string(out)})
}

func cmdCertRenew(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireCertbot(); ed != nil {
		return nil, ed
	}
	name, _ := args["subject"].(string)
	if name == "" {
		name, _ = args["name"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	out, err := exec.Command("certbot", "renew", "--cert-name", name, "--non-interactive").CombinedOutput()
	if err != nil {
		return failResult("CALLABLE_FAILED", "certbot renew: "+err.Error()+"\n"+string(out))
	}
	return okResult(map[string]interface{}{"name": name, "renewed": true, "output": string(out)})
}

func cmdCertRemove(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) {
	if ed := requireCertbot(); ed != nil {
		return nil, ed
	}
	name, _ := args["subject"].(string)
	if name == "" {
		name, _ = args["name"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return failResult("INVALID_PARAMS", "name is required")
	}
	out, err := exec.Command("certbot", "delete", "--cert-name", name, "--non-interactive").CombinedOutput()
	if err != nil {
		return failResult("CALLABLE_FAILED", "certbot delete: "+err.Error()+"\n"+string(out))
	}
	return okResult(map[string]interface{}{"name": name, "removed": true, "output": string(out)})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func requireCertbot() *errorDetail {
	if _, err := exec.LookPath("certbot"); err != nil {
		return &errorDetail{Code: "CALLABLE_FAILED", Message: "certbot binary not found in PATH — install certbot"}
	}
	return nil
}
