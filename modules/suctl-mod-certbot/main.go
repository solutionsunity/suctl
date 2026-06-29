// SPDX-License-Identifier: Apache-2.0

// suctl-mod-certbot manages Let's Encrypt / ACME certificates by wrapping the
// certbot CLI and reading /etc/letsencrypt/. It is a separate suctl module
// from suctl-mod-nginx so cert lifecycle stays decoupled from web-server
// configuration.
//
// The module inherits one bidirectional broker wire from core via
// SUCTL_BROKER_FD; modserver serves core's requests and makes its
// own calls over that single wire. This process listens on no socket.
package main

import (
	"fmt"
	"os"

	"github.com/solutionsunity/suctl/sdk/modserver"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

type errorDetail = protocol.ErrorDetail

var manifestJSON []byte

func init() {
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		data = []byte(`{"version":"0.1.0","protocol":"1","platform":["linux"],"author":"suctl","license":"Apache-2.0","entrypoint":"suctl-mod-certbot","description":"certbot module","capabilities":[]}`)
	}
	manifestJSON = data
}

func main() {
	handlers := map[string]modserver.Handler{
		"certbot.cert.provision": cmdCertProvision,
		"certbot.cert.list":      cmdCertList,
		"certbot.cert.read":      cmdCertRead,
		"certbot.cert.renew":     cmdCertRenew,
		"certbot.cert.remove":    cmdCertRemove,
	}

	if err := modserver.Serve(modserver.Config{
		Name:     "suctl-mod-certbot",
		Manifest: manifestJSON,
		Handlers: handlers,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "suctl-mod-certbot: %v\n", err)
		os.Exit(1)
	}
}

// Result helpers are aliased to the SDK so handlers don't duplicate them.
var (
	okResult   = protocol.OKResult
	failResult = protocol.FailResult
)
