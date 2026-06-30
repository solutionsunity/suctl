// SPDX-License-Identifier: Apache-2.0

// suctl-mod-os is a suctl module that manages systemd services on the host,
// exposing the service surface (register / start / stop / restart).
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
		data = []byte(`{"version":"0.1.0","protocol":"1","platform":["linux"],"author":"suctl","license":"Apache-2.0","entrypoint":"suctl-mod-os","description":"OS diagnostics module","capabilities":[]}`)
	}
	manifestJSON = data
}

func main() {
	handlers := map[string]modserver.Handler{
		"os.service.register":   cmdServiceRegister,
		"os.service.unregister": cmdServiceUnregister,
		"os.service.start":      func(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) { return cmdServiceControl(ctx, args, "start") },
		"os.service.stop":       func(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) { return cmdServiceControl(ctx, args, "stop") },
		"os.service.restart":    func(ctx context.Context, args map[string]interface{}) (interface{}, *errorDetail) { return cmdServiceControl(ctx, args, "restart") },
		"os.service.survey":     cmdServiceSurvey,
		"os.service.focus":      cmdServiceFocus,
	}

	if err := modserver.Serve(modserver.Config{
		Name:     "suctl-mod-os",
		Manifest: manifestJSON,
		Handlers: handlers,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "suctl-mod-os: %v\n", err)
		os.Exit(1)
	}
}

// Result helpers aliased to the SDK helpers.
var (
	okResult   = protocol.OKResult
	failResult = protocol.FailResult
)
