package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type OperatorTestCtx struct {
	*TestContext
}

func odhOperatorTestSuite(t *testing.T) {
	t.Helper()

	// Initialize the test context.
	tc, err := NewTestContext(t)
	require.NoError(t, err, "Failed to initialize test context")

	// Create an instance of test context.
	operatorTestCtx := OperatorTestCtx{
		TestContext: tc,
	}

	// Define test cases.
	testCases := []TestCase{
		{"Validate CRDs owned by the operator", operatorTestCtx.ValidateOwnedCRDs},
	}

	// Run the test suite.
	RunTestCases(t, testCases)
}

// ValidateOwnedCRDs validates if the platform-owned CRDs are properly created and available.
func (tc *OperatorTestCtx) ValidateOwnedCRDs(t *testing.T) {
	t.Helper()

	crdsTestCases := []struct {
		name string
		crd  string
	}{
		{"DataScienceCluster CRD", "datascienceclusters.datasciencecluster.opendatahub.io"},
		{"DSCInitialization CRD", "dscinitializations.dscinitialization.opendatahub.io"},
		{"Platform CRD", "platforms.config.opendatahub.io"},
		{"Auth CRD", "auths.services.platform.opendatahub.io"},
	}

	for _, testCase := range crdsTestCases {
		t.Run("Validate "+testCase.name, func(t *testing.T) {
			t.Parallel()
			tc.EnsureCRDEstablished(testCase.crd)
		})
	}
}
