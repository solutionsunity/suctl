// SPDX-License-Identifier: Apache-2.0

package health

import (
	"encoding/json"
	"time"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// StandardResponse returns the JSON bytes for a standard healthy module response.
// It includes the status "healthy" and the uptime calculated from startTime.
func StandardResponse(startTime time.Time) ([]byte, error) {
	return json.Marshal(protocol.HealthResult{
		Status: "healthy",
		Uptime: int(time.Since(startTime).Seconds()),
	})
}
