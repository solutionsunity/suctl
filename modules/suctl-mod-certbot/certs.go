// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	letsEncryptRoot   = "/etc/letsencrypt"
	renewalDir        = "/etc/letsencrypt/renewal"
	liveDir           = "/etc/letsencrypt/live"
	expiringThreshold = 14 // days
)

// CertStatus is the derived health of a certificate.
type CertStatus string

const (
	StatusValid    CertStatus = "valid"
	StatusExpiring CertStatus = "expiring"
	StatusExpired  CertStatus = "expired"
	StatusMissing  CertStatus = "missing" // renewal conf exists but PEM unreadable
)

// CertInfo is the derived state of one certbot-managed certificate lineage.
type CertInfo struct {
	Name        string     `json:"name"`
	Domains     []string   `json:"domains"`
	DaysLeft    int        `json:"days_left"`
	NotBefore   string     `json:"not_before,omitempty"`
	NotAfter    string     `json:"not_after,omitempty"`
	Issuer      string     `json:"issuer,omitempty"`
	Status      CertStatus `json:"status"`
	LivePath    string     `json:"live_path,omitempty"`
	RenewalConf string     `json:"renewal_conf"`
}

// renewalConfPath returns the path of the renewal config for a cert name.
func renewalConfPath(name string) string { return filepath.Join(renewalDir, name+".conf") }

// livePEMPath returns the path of the fullchain.pem for a cert lineage.
func livePEMPath(name string) string { return filepath.Join(liveDir, name, "fullchain.pem") }

// ListCerts discovers every certbot-managed certificate lineage by scanning
// /etc/letsencrypt/renewal/*.conf and returns derived CertInfo for each.
// Returns an empty slice (no error) if the renewal dir does not exist.
func ListCerts() ([]*CertInfo, error) {
	entries, err := os.ReadDir(renewalDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", renewalDir, err)
	}
	var out []*CertInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".conf")
		out = append(out, ReadCert(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadCert builds a CertInfo from the live PEM and renewal conf. Never returns
// nil — when the PEM is missing or unparseable, status is StatusMissing.
func ReadCert(name string) *CertInfo {
	ci := &CertInfo{
		Name:        name,
		RenewalConf: renewalConfPath(name),
		LivePath:    filepath.Join(liveDir, name),
		Status:      StatusMissing,
	}
	data, err := os.ReadFile(livePEMPath(name))
	if err != nil {
		return ci
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return ci
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ci
	}
	ci.Domains = uniqueDomains(cert.Subject.CommonName, cert.DNSNames)
	ci.NotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
	ci.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	ci.Issuer = cert.Issuer.CommonName
	ci.DaysLeft = int(time.Until(cert.NotAfter).Hours() / 24)
	ci.Status = deriveStatus(ci.DaysLeft)
	return ci
}

// deriveStatus converts days-left into the public status enum.
func deriveStatus(daysLeft int) CertStatus {
	switch {
	case daysLeft <= 0:
		return StatusExpired
	case daysLeft < expiringThreshold:
		return StatusExpiring
	default:
		return StatusValid
	}
}

// uniqueDomains returns the cert's domain list with CN first when present and
// no duplicates. Order otherwise matches the cert's DNSNames.
func uniqueDomains(cn string, sans []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(sans)+1)
	if cn != "" {
		seen[cn] = true
		out = append(out, cn)
	}
	for _, d := range sans {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
