package e2e_test

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
)

func randomModuleRegistration(t *testing.T, source modules.ConfigSource) modulebuiltin.ModuleRegistration {
	t.Helper()

	candidates := make([]modulebuiltin.ModuleRegistration, 0)
	for _, registration := range modulebuiltin.Registrations() {
		if registration.ConfigSource == source {
			candidates = append(candidates, registration)
		}
	}
	if len(candidates) == 0 {
		t.Fatalf("no built-in module registrations found for config source %d", source)
	}

	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
	if err != nil {
		t.Fatalf("choose random %s module: %v", configSourceName(source), err)
	}
	registration := candidates[index.Int64()]
	t.Logf("Selected random %s module %q for single-module coverage", configSourceName(source), registration.Handler.GetName())
	return registration
}

func moduleManagementField(registration modulebuiltin.ModuleRegistration, source modules.ConfigSource) string {
	if source != modules.ConfigFromDSC {
		return registration.Handler.GetName()
	}
	return dscComponentField(registration.Handler.GetName())
}
