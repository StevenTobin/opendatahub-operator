package e2e_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	dscApi "github.com/opendatahub-io/opendatahub-operator/v2/api/datasciencecluster/v3"
	dsciv2 "github.com/opendatahub-io/opendatahub-operator/v2/api/dscinitialization/v2"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/resources"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"
)

// ModuleFocusedTestCtx owns the state for one isolated module lifecycle. It
// deliberately does not embed the DAG test context: focused mode must not
// inherit platform-wide upgrade, gate, or quota behavior.
type ModuleFocusedTestCtx struct {
	*TestContext

	registration modulebuiltin.ModuleRegistration
	report       *moduleContractReport
}

func moduleFocusedTestSuite(t *testing.T, registration modulebuiltin.ModuleRegistration) {
	t.Helper()
	skipUnless(t, Tier2)

	tc, err := NewTestContext(t)
	require.NoError(t, err, "initialize focused module test context")
	if tc.IsXKS() {
		t.Skip("focused module coverage is only supported on OpenShift")
	}

	// Focused mode assumes the platform CRs already exist. This check is made
	// before registering cleanup so a missing prerequisite cannot mutate the
	// cluster or hide the actionable setup failure.
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DSCInitialization, tc.DSCInitializationNamespacedName),
		WithCustomErrorMsg("DSCInitialization must exist before focused module tests"),
	)
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCustomErrorMsg("DataScienceCluster must exist before focused module tests"),
	)
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      tc.getControllerDeploymentName(),
		}),
		WithCondition(jq.Match(`any(.status.conditions[]; .type == "Available" and .status == "True")`)),
		WithEventuallyTimeout(tc.TestTimeouts.mediumEventuallyTimeout),
		WithCustomErrorMsg("platform operator Deployment must be available before focused module tests"),
	)

	ensureOperatorPrerequisitesFor(t, tc, []modulebuiltin.ModuleRegistration{registration}, false)

	ctx := &ModuleFocusedTestCtx{
		TestContext:  tc,
		registration: registration,
		report:       &moduleContractReport{},
	}

	t.Run(registration.Handler.GetName(), func(t *testing.T) {
		// Always return the shared platform to an all-Removed module baseline.
		// Register this before the first mutation, including the baseline itself.
		t.Cleanup(func() {
			setAllRemovedFor(t, tc)
		})

		t.Run("Prepare_clean_module_baseline", func(t *testing.T) {
			setAllRemovedFor(t, tc)
		})
		t.Run("Enable_selected_module", func(t *testing.T) {
			ctx.enableSelectedModule(t)
		})
		t.Run("Validate_selected_module_contract", func(t *testing.T) {
			ctx.validateSelectedModule(t)
		})
		t.Run("Validate_selected_CR_spec_projection", func(t *testing.T) {
			ctx.validateSelectedCRSpecProjection(t)
		})
		t.Run("Validate_platform-owned_resource_recovery", func(t *testing.T) {
			validatePlatformOwnedResourceRecovery(t, tc, registration)
		})
		t.Run("Disable_and_cleanup_selected_module", func(t *testing.T) {
			ctx.disableSelectedModule(t)
		})
	})
}

func (ctx *ModuleFocusedTestCtx) enableSelectedModule(t *testing.T) {
	t.Helper()
	registration := ctx.registration
	if registration.ConfigSource == modules.ConfigFromDSCI {
		setDSCIMonitoringStateFor(t, ctx.TestContext, operatorv1.Managed)
	} else {
		field := moduleManagementField(registration, modules.ConfigFromDSC)
		ctx.EventuallyResourcePatched(
			WithMinimalObject(gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName),
			WithMutateFunc(selectComponentsTransform(string(operatorv1.Managed), []string{field})),
		)
	}

	ctx.waitForSourceGeneration(t)
	ctx.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, ctx.PlatformNamespacedName),
		WithCondition(jq.Match(
			`.spec.modules.%s.managementState == "%s"`,
			registration.Handler.GetName(), operatorv1.Managed,
		)),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("Platform should project selected module %s as Managed", registration.Handler.GetName()),
	)

	ctx.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: ctx.AppsNamespace,
			Name:      moduleDeploymentName(registration.Handler),
		}),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("selected module %s operator Deployment should be created", registration.Handler.GetName()),
	)

	// Reading the canonical module CR also proves that its CRD is served. The
	// generic contract performs the owner-reference assertion later.
	ctx.EnsureResourcesExist(
		WithMinimalObject(registration.Handler.GetGVK(), types.NamespacedName{}),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("selected module %s CRD and canonical CR should be available", registration.Handler.GetName()),
	)
}

func (ctx *ModuleFocusedTestCtx) validateSelectedModule(t *testing.T) {
	t.Helper()
	contractCtx := newModuleContractTestCtx(t, ctx.TestContext, ctx.registration, ctx.report)
	contractCtx.ValidateModuleContract(t)
	ctx.report.log(t)
}

func (ctx *ModuleFocusedTestCtx) validateSelectedCRSpecProjection(t *testing.T) {
	t.Helper()

	contractCtx := newModuleContractTestCtx(t, ctx.TestContext, ctx.registration, ctx.report)
	actual := contractCtx.discoverOwnedModuleCR(t)

	dscCtx := &modules.DSCContext{}
	if ctx.registration.ConfigSource == modules.ConfigFromDSCI {
		dsci := &dsciv2.DSCInitialization{}
		require.NoError(t, ctx.Client().Get(ctx.Context(), ctx.DSCInitializationNamespacedName, dsci), "get DSCI for module spec projection")
		dscCtx.DSCI = dsci
	} else {
		dsc := &dscApi.DataScienceCluster{}
		require.NoError(t, ctx.Client().Get(ctx.Context(), ctx.DataScienceClusterNamespacedName, dsc), "get DSC for module spec projection")
		dscCtx.DSC = dsc
	}

	gatewayDomain, _ := resources.GetGatewayDomain(ctx.Context(), ctx.Client())
	expected, err := ctx.registration.Handler.BuildModuleCR(ctx.Context(), ctx.Client(), dscCtx, &modules.ModuleCRConfig{
		ApplicationsNamespace: ctx.AppsNamespace,
		GatewayDomain:         gatewayDomain,
		Release:               cluster.GetRelease(),
	})
	require.NoError(t, err, "build expected %s module CR", ctx.registration.Handler.GetName())
	require.NotNil(t, expected, "expected %s module CR should not be nil", ctx.registration.Handler.GetName())

	expectedSpec, _, err := unstructured.NestedFieldCopy(expected.Object, "spec")
	require.NoError(t, err, "read expected %s module CR spec", ctx.registration.Handler.GetName())
	actualSpec, _, err := unstructured.NestedFieldCopy(actual.Object, "spec")
	require.NoError(t, err, "read actual %s module CR spec", ctx.registration.Handler.GetName())
	requireFocusedSpecSubset(t, expectedSpec, actualSpec, "spec")
}

func (ctx *ModuleFocusedTestCtx) disableSelectedModule(t *testing.T) {
	t.Helper()
	registration := ctx.registration
	if registration.ConfigSource == modules.ConfigFromDSCI {
		setDSCIMonitoringStateFor(t, ctx.TestContext, operatorv1.Removed)
	} else {
		field := moduleManagementField(registration, modules.ConfigFromDSC)
		ctx.EventuallyResourcePatched(
			WithMinimalObject(gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName),
			WithMutateFunc(selectComponentsTransform(string(operatorv1.Removed), []string{field})),
		)
	}

	ctx.waitForSourceGeneration(t)
	ctx.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, ctx.PlatformNamespacedName),
		WithCondition(jq.Match(
			`.spec.modules.%s.managementState == "%s"`,
			registration.Handler.GetName(), operatorv1.Removed,
		)),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
	)
	ctx.EnsureResourcesGone(
		WithMinimalObject(registration.Handler.GetGVK(), types.NamespacedName{}),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("selected module %s CR should be removed", registration.Handler.GetName()),
	)
	ctx.EnsureResourceGone(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: ctx.AppsNamespace,
			Name:      moduleDeploymentName(registration.Handler),
		}),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("selected module %s operator Deployment should be removed", registration.Handler.GetName()),
	)
	ctx.EnsureResourceGone(
		WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
			Namespace: ctx.AppsNamespace,
			Name:      modules.PlatformConfigName(registration.Handler.GetName()),
		}),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("selected module %s platform handoff ConfigMap should be removed", registration.Handler.GetName()),
	)
}

func (ctx *ModuleFocusedTestCtx) waitForSourceGeneration(t *testing.T) {
	t.Helper()
	if ctx.registration.ConfigSource == modules.ConfigFromDSCI {
		ensureDSCIReconciledFor(t, ctx.TestContext)
		return
	}
	ctx.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName),
		WithCondition(jq.Match(`.status.observedGeneration == .metadata.generation`)),
		WithEventuallyTimeout(ctx.TestTimeouts.componentReadinessTimeout),
		WithEventuallyPollingInterval(10*time.Second),
	)
}

// requireFocusedSpecSubset compares fields the production handler requested,
// while tolerating API-server defaults and module-owned fields added after the
// platform creates the CR.
func requireFocusedSpecSubset(t *testing.T, expected, actual any, path string) {
	t.Helper()
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		require.True(t, ok, "%s should be an object", path)
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			value := expectedValue[key]
			if value == nil {
				continue
			}
			requireFocusedSpecSubset(t, value, actualValue[key], fmt.Sprintf("%s.%s", path, key))
		}
	case []any:
		actualValue, ok := actual.([]any)
		require.True(t, ok, "%s should be an array", path)
		require.Equal(t, expectedValue, actualValue, "%s should match platform desired state", path)
	default:
		require.Equal(t, expectedValue, actual, "%s should match platform desired state", path)
	}
}
