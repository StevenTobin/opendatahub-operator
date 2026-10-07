package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opendatahub-io/opendatahub-operator/v2/cmd/test-retry/pkg/config"
)

func TestE2EDefaultSkipAtPrefixes(t *testing.T) {
	cmd := NewE2ECommand(&config.Config{})

	prefixes, err := cmd.Flags().GetStringSlice("skip-at-prefix")
	require.NoError(t, err)
	require.Equal(t, []string{
		"TestOdhOperator/monitoring/",
		"TestOdhOperator/services/*/",
		"TestOdhOperator/DAG_Ordering_E2E_Tests/Validate_runlevel_gating_and_convergence/*/",
		"TestOdhOperator/",
	}, prefixes)
	require.NotContains(t, prefixes, "TestOdhOperator/Platform_Orchestration_E2E_Tests/*/")
	require.NotContains(t, prefixes, "TestOdhOperator/DAG_Ordering_E2E_Tests/")
}
