// SPDX-License-Identifier: Apache-2.0

// suctl-mod-nginx is a suctl module that manages nginx virtual hosts,
// SSL certificates via certbot, and per-domain availability state.
//
// The module inherits one bidirectional broker wire from core via
// SUCTL_BROKER_FD; modserver serves core's requests and makes its
// own calls over that single wire. This process listens on no socket.
package main

import (
	"context"
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
		data = []byte(`{"version":"0.1.0","protocol":"1","platform":["linux"],"author":"suctl","license":"Apache-2.0","entrypoint":"suctl-mod-nginx","description":"nginx module","capabilities":[]}`)
	}
	manifestJSON = data
}

func main() {
	handlers := map[string]modserver.Handler{
		"nginx.domain.add":         cmdDomainAdd,
		"nginx.domain.list":        cmdDomainList,
		"nginx.domain.suspend":     cmdSuspend,
		"nginx.domain.unsuspend":   cmdUnsuspend,
		"nginx.maintenance.enable": func(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) { return cmdMaintenance(ctx, args, true) },
		"nginx.maintenance.disable": func(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) { return cmdMaintenance(ctx, args, false) },
		"nginx.reload":             cmdReload,
		"nginx.domain.survey":      cmdReplSurvey,
		"nginx.domain.focus":       cmdReplFocus,
		"nginx.provision":          cmdProvision,
		"nginx.source.survey":      cmdSourceSurvey,
		"nginx.source.focus":       cmdSourceFocus,
		"nginx.source.migrate":     cmdSourceMigrate,
		"nginx.block.survey":       cmdBlockSurvey,
	}

	if err := modserver.Serve(modserver.Config{
		Name:     "suctl-mod-nginx",
		Manifest: manifestJSON,
		Handlers: handlers,

	}); err != nil {
		fmt.Fprintf(os.Stderr, "suctl-mod-nginx: %v\n", err)
		os.Exit(1)
	}
}

// Result helpers aliased to the SDK helpers.
var (
	okResult   = protocol.OKResult
	failResult = protocol.FailResult
)
