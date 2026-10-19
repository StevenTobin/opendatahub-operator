package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"

	. "github.com/onsi/gomega"
)

const (
	kserveLLMInferenceWebhookServiceName = "llmisvc-webhook-server-service"
	kserveLLMInferenceWebhookConfigName  = "llminferenceserviceconfig.serving.kserve.io"
)

// CleanupPreviousTestResources removes leftover resources from previous test runs.
// This is called at the start of test suites to ensure a clean environment.
// It removes platform-owned singleton resources before deleting test namespaces.
func CleanupPreviousTestResources(t *testing.T) {
	t.Helper()

	// Initialize the test context.
	tc, err := NewTestContext(t)
	require.NoError(t, err, "Failed to initialize test context")

	// Cleanup existing DataScienceCluster and DSCInitialization (single instances)
	cleanupCoreOperatorResources(t, tc)

	// Core owner deletion above waits for controller finalizers and module cleanup
	// before the shared applications namespace is removed.
	cleanupStaleKServeLLMInferenceResources(t, tc)
	t.Logf("Cleaning up applications namespace: %s", tc.AppsNamespace)
	tc.DeleteResource(
		WithMinimalObject(gvk.Namespace, types.NamespacedName{Name: tc.AppsNamespace}),
		WithIgnoreNotFound(true),
		WithWaitForDeletion(true),
	)

	// Cleanup CodeFlare resources
	cleanupCodeFlareTestResources(t, tc)

	// Cleanup stale upgrade-gate ConfigMaps left by interrupted test runs
	cleanupGateSourceConfigMaps(t, tc)
}

// cleanupCoreOperatorResources deletes DataScienceCluster and DSCInitialization resources.
func cleanupCoreOperatorResources(t *testing.T, tc *TestContext) {
	t.Helper()

	// The current default is v3. Try both versions so cleanup also handles a
	// cluster left behind by an older v2 installation.
	t.Logf("Cleaning up DataScienceCluster instance %s", tc.DataScienceClusterNamespacedName.Name)
	for _, dscGVK := range []schema.GroupVersionKind{gvk.DataScienceCluster, gvk.DataScienceClusterV2} {
		dsc := &unstructured.Unstructured{}
		dsc.SetGroupVersionKind(dscGVK)
		dsc.SetName(tc.DataScienceClusterNamespacedName.Name)
		err := tc.Client().Delete(tc.Context(), dsc)
		if err != nil && !k8serr.IsNotFound(err) && !meta.IsNoMatchError(err) {
			require.NoError(t, err, "Failed to delete DataScienceCluster %s using %s", dsc.GetName(), dscGVK.GroupVersion())
		}
	}

	deleteDSCIAfterDSCDeletion(t, tc)

	// Delete Auth CR (cluster-scoped, not affected by namespace deletion).
	tc.DeleteResources(
		WithMinimalObject(gvk.Auth, types.NamespacedName{}),
		WithWaitForDeletion(true),
		WithIgnoreNotFound(true),
		WithAcceptableErr(meta.IsNoMatchError, "IsNoMatchError"),
	)
}

// deleteDSCIAfterDSCDeletion retries the DSCI delete while the validating
// webhook observes the DSC during its asynchronous deletion window.
func deleteDSCIAfterDSCDeletion(t *testing.T, tc *TestContext) {
	t.Helper()

	nn := tc.DSCInitializationNamespacedName
	tc.g.Eventually(func() error {
		dsci := &unstructured.Unstructured{}
		dsci.SetGroupVersionKind(gvk.DSCInitialization)
		dsci.SetName(nn.Name)
		deleteErr := tc.Client().Delete(tc.Context(), dsci)
		if k8serr.IsNotFound(deleteErr) {
			return nil
		}
		return deleteErr
	}).WithTimeout(tc.TestTimeouts.mediumEventuallyTimeout).
		WithPolling(tc.TestTimeouts.defaultEventuallyPollInterval).
		Should(Succeed(), "DSCInitialization deletion remained blocked")

	tc.EnsureResourceGone(
		WithMinimalObject(gvk.DSCInitialization, nn),
		WithCustomErrorMsg("DSCInitialization instance %s was not fully deleted", nn.Name),
	)
}

// cleanupStaleKServeLLMInferenceResources removes stale KServe resources
// that can block namespace deletion after the KServe module has disappeared.
// This can happen in a failed previous test and is generally unexpected.
func cleanupStaleKServeLLMInferenceResources(t *testing.T, tc *TestContext) {
	t.Helper()

	service := &unstructured.Unstructured{}
	service.SetGroupVersionKind(gvk.Service)
	err := tc.Client().Get(tc.Context(), types.NamespacedName{
		Namespace: tc.AppsNamespace,
		Name:      kserveLLMInferenceWebhookServiceName,
	}, service)
	if err == nil {
		return
	}
	if !k8serr.IsNotFound(err) && !meta.IsNoMatchError(err) {
		require.NoError(t, err, "failed to check KServe LLMInferenceServiceConfig webhook Service")
		return
	}

	t.Logf("Removing stale KServe LLMInferenceServiceConfig webhook configuration %s", kserveLLMInferenceWebhookConfigName)
	tc.DeleteResource(
		WithMinimalObject(gvk.ValidatingWebhookConfiguration, types.NamespacedName{Name: kserveLLMInferenceWebhookConfigName}),
		WithIgnoreNotFound(true),
		WithWaitForDeletion(false),
		WithAcceptableErr(meta.IsNoMatchError, "IsNoMatchError"),
	)

	for _, configGVK := range []schema.GroupVersionKind{
		gvk.LLMInferenceServiceConfigV1Alpha2,
		gvk.LLMInferenceServiceConfigV1Alpha1,
	} {
		for _, config := range tc.FetchResources(
			WithMinimalObject(configGVK, types.NamespacedName{Namespace: tc.AppsNamespace}),
			WithListOptions(&client.ListOptions{Namespace: tc.AppsNamespace}),
		) {
			t.Logf("Removing stale KServe LLMInferenceServiceConfig %s/%s finalizer", config.GetNamespace(), config.GetName())
			tc.DeleteResource(
				WithMinimalObject(configGVK, types.NamespacedName{
					Namespace: config.GetNamespace(),
					Name:      config.GetName(),
				}),
				WithIgnoreNotFound(true),
				WithWaitForDeletion(false),
				WithRemoveFinalizersOnDelete(true),
				WithAcceptableErr(meta.IsNoMatchError, "IsNoMatchError"),
			)
		}
	}
}

func cleanupGateSourceConfigMaps(t *testing.T, tc *TestContext) {
	t.Helper()

	for _, name := range []string{"e2e-gate-source-1", "e2e-gate-source-2"} {
		tc.DeleteResource(
			WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
				Namespace: tc.OperatorNamespace,
				Name:      name,
			}),
			WithIgnoreNotFound(true),
			WithWaitForDeletion(false),
		)
	}
}

func cleanupCodeFlareTestResources(t *testing.T, tc *TestContext) {
	t.Helper()

	// Cleanup CodeFlare resources
	t.Logf("Cleaning up CodeFlare resources")
	tc.DeleteResource(
		WithMinimalObject(gvk.CodeFlare, types.NamespacedName{Name: defaultCodeFlareComponentName}),
		WithIgnoreNotFound(true),
		WithWaitForDeletion(false),
		WithAcceptableErr(meta.IsNoMatchError, "IsNoMatchError"),
	)
}
