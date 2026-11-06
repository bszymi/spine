//go:build !goexperiment.jsonv2

package workflow

import (
	"encoding/json"
	"testing"
)

// TestRefuseUnencodable_EqualsLegacyEncoder shows that refuseUnencodable
// refuses exactly what the legacy encoding/json engine (Go 1.26, or Go
// 1.27 with GOEXPERIMENT=nojsonv2) refuses for every case: the explicit
// check is that engine's behaviour, not a new rule (INIT-024 TASK-014).
// The v2-backed engine accepts several of these cases, so the file is
// built only without it.
func TestRefuseUnencodable_EqualsLegacyEncoder(t *testing.T) {
	for _, tc := range unencodableCases {
		t.Run(tc.name, func(t *testing.T) {
			_, marshalErr := json.Marshal(tc.v)
			refused := refuseUnencodable(tc.v, "") != nil
			if refused != (marshalErr != nil) {
				t.Fatalf("refuseUnencodable refused=%v, legacy json.Marshal error=%v", refused, marshalErr)
			}
		})
	}
}
