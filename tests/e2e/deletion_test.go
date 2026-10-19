package e2e_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/require"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/status"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"

	. "github.com/onsi/gomega"
)

const (
	platformFinalizer           = "platform.opendatahub.io/finalizer"
	moduleDeletionHoldFinalizer = "tests.opendatahub.io/module-deletion-hold"
)

type DeletionTestCtx struct {
	*TestContext
}

type discoveredModuleCR struct {
	ModuleName     string
	GVK            schema.GroupVersionKind
	NamespacedName types.NamespacedName
	UID            types.UID
}

// deletionTestSuite runs the deletion test suite.
func deletionTestSuite(t *testing.T) {
	t.Helper()

	// Initialize the test context.
	tc, err := NewTestContext(t)
	require.NoError(t, err, "Failed to initialize test context")

	// Create an instance of test context.
	deletionTestCtx := DeletionTestCtx{
		TestContext: tc,
	}

	// Define the test cases
	testCases := []TestCase{
		{"Delete DataScienceCluster instance", deletionTestCtx.TestDSCDeletion},
		{"Delete DSCInitialization instance", deletionTestCtx.TestDSCIDeletion},
	}

	// Run the test suite.
	RunTestCases(t, testCases)
}

// TestDSCDeletion verifies that DSC-backed module CRs are deleted while the
// DSCI-backed module source and shared Platform remain.
func (tc *DeletionTestCtx) TestDSCDeletion(t *testing.T) {
	t.Helper()

	tc.SkipIfXKSCluster(t)
	skipUnless(t, Tier3)

	// The deletion suite can run with DSC management tests disabled. Ensure the
	// v3 DataScienceCluster exists before discovering owned module CRs.
	tc.EventuallyResourceCreatedOrUpdated(
		WithObjectToCreate(CreateDSC(tc.DataScienceClusterNamespacedName.Name, tc.WorkbenchesNamespace)),
		WithCondition(jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, status.ConditionTypeReady, metav1.ConditionTrue)),
		WithEventuallyTimeout(tc.TestTimeouts.crCreationTimeout),
		WithEventuallyPollingInterval(tc.TestTimeouts.defaultEventuallyPollInterval),
		WithCustomErrorMsg("DataScienceCluster %s should be Ready before deletion coverage", tc.DataScienceClusterNamespacedName.Name),
	)

	dsci := tc.EnsureResourceExists(
		WithMinimalObject(gvk.DSCInitialization, tc.DSCInitializationNamespacedName),
	)
	dsc := tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
	)

	// Ensure the test is non-vacuous without depending on any particular module.
	// Existing module CRs are also discovered and included in the assertions.
	tc.enableFirstModuleForSource(t, modules.ConfigFromDSC)
	tc.enableFirstModuleForSource(t, modules.ConfigFromDSCI)

	dscModuleCRs := tc.waitForOwnedModuleCRs(t, modules.ConfigFromDSC, dsc)
	dsciModuleCRs := tc.waitForOwnedModuleCRs(t, modules.ConfigFromDSCI, dsci)
	platform := tc.waitForPlatformOwnedBy(t, dsci, dsc)

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, namespacedNameFor(platform)),
		WithCondition(jq.Match(`.metadata.deletionTimestamp == null and (.metadata.finalizers | index("%s")) != null`, platformFinalizer)),
	)

	t.Logf("Deleting DataScienceCluster after discovering %d DSC-backed and %d DSCI-backed module CRs", len(dscModuleCRs), len(dsciModuleCRs))
	tc.DeleteResource(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithWaitForDeletion(true),
	)

	for _, moduleCR := range dscModuleCRs {
		tc.EnsureResourceGone(
			WithMinimalObject(moduleCR.GVK, moduleCR.NamespacedName),
			WithEventuallyTimeout(5*time.Minute),
			WithEventuallyPollingInterval(10*time.Second),
			WithCustomErrorMsg("DSC-backed module CR %s %s was not deleted with its DSC owner", moduleCR.ModuleName, moduleCR.NamespacedName),
		)
	}

	tc.EnsureResourceExistsConsistently(
		WithMinimalObject(gvk.DSCInitialization, namespacedNameFor(dsci)),
		WithCondition(jq.Match(`.metadata.deletionTimestamp == null and .metadata.uid == "%s"`, dsci.GetUID())),
		WithConsistentlyDuration(30*time.Second),
		WithConsistentlyPollingInterval(5*time.Second),
	)

	for _, moduleCR := range dsciModuleCRs {
		tc.EnsureResourceExistsConsistently(
			WithMinimalObject(moduleCR.GVK, moduleCR.NamespacedName),
			WithCondition(jq.Match(
				`.metadata.deletionTimestamp == null and .metadata.uid == "%s" and any(.metadata.ownerReferences[]; .uid == "%s")`,
				moduleCR.UID, dsci.GetUID(),
			)),
			WithConsistentlyDuration(30*time.Second),
			WithConsistentlyPollingInterval(5*time.Second),
			WithCustomErrorMsg("DSCI-backed module CR %s %s should survive DSC deletion", moduleCR.ModuleName, moduleCR.NamespacedName),
		)
	}

	tc.EnsureResourceExistsConsistently(
		WithMinimalObject(gvk.Platform, namespacedNameFor(platform)),
		WithCondition(jq.Match(
			`.metadata.deletionTimestamp == null and .metadata.uid == "%s" and (.metadata.finalizers | index("%s")) != null and any(.metadata.ownerReferences[]; .uid == "%s")`,
			platform.GetUID(), platformFinalizer, dsci.GetUID(),
		)),
		WithConsistentlyDuration(30*time.Second),
		WithConsistentlyPollingInterval(5*time.Second),
	)
}

// TestDSCIDeletion verifies DSCI-backed module CR cleanup and that the Platform
// finalizer holds Platform deletion until module CR finalizers have completed.
func (tc *DeletionTestCtx) TestDSCIDeletion(t *testing.T) {
	t.Helper()

	tc.SkipIfXKSCluster(t)
	skipUnless(t, Tier3)

	dsci := tc.EnsureResourceExists(
		WithMinimalObject(gvk.DSCInitialization, tc.DSCInitializationNamespacedName),
	)
	dsciModuleCRs := tc.waitForOwnedModuleCRs(t, modules.ConfigFromDSCI, dsci)
	platform := tc.waitForPlatformOwnedBy(t, dsci)

	heldModuleCR := dsciModuleCRs[0]
	t.Logf("Holding discovered DSCI-backed module CR %s %s to observe Platform finalizer behavior", heldModuleCR.ModuleName, heldModuleCR.NamespacedName)
	tc.addModuleDeletionHoldFinalizer(heldModuleCR)

	finalizerReleased := false
	defer func() {
		if !finalizerReleased {
			tc.removeModuleDeletionHoldFinalizer(heldModuleCR, true)
		}
	}()

	tc.DeleteResource(
		WithMinimalObject(gvk.DSCInitialization, namespacedNameFor(dsci)),
	)

	tc.EnsureResourceExists(
		WithMinimalObject(heldModuleCR.GVK, heldModuleCR.NamespacedName),
		WithCondition(jq.Match(
			`.metadata.deletionTimestamp != null and (.metadata.finalizers | index("%s")) != null`,
			moduleDeletionHoldFinalizer,
		)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(5*time.Second),
	)
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, namespacedNameFor(platform)),
		WithCondition(jq.Match(
			`.metadata.deletionTimestamp != null and (.metadata.finalizers | index("%s")) != null and any(.metadata.ownerReferences[]; .uid == "%s")`,
			platformFinalizer, dsci.GetUID(),
		)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(5*time.Second),
		WithCustomErrorMsg("Platform should retain its finalizer while a DSCI-backed module CR is still terminating"),
	)

	tc.removeModuleDeletionHoldFinalizer(heldModuleCR, false)
	finalizerReleased = true

	for _, moduleCR := range dsciModuleCRs {
		tc.EnsureResourceGone(
			WithMinimalObject(moduleCR.GVK, moduleCR.NamespacedName),
			WithEventuallyTimeout(5*time.Minute),
			WithEventuallyPollingInterval(10*time.Second),
			WithCustomErrorMsg("DSCI-backed module CR %s %s was not deleted with its DSCI owner", moduleCR.ModuleName, moduleCR.NamespacedName),
		)
	}

	tc.EnsureResourceGone(
		WithMinimalObject(gvk.Platform, namespacedNameFor(platform)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
		WithCustomErrorMsg("Platform was not deleted after all module CR finalizers completed"),
	)
	tc.EnsureResourceGone(
		WithMinimalObject(gvk.DSCInitialization, namespacedNameFor(dsci)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
}

func (tc *DeletionTestCtx) enableFirstModuleForSource(t *testing.T, source modules.ConfigSource) {
	t.Helper()

	var parentGVK schema.GroupVersionKind
	var parentNN types.NamespacedName
	var fieldPathPrefix []string

	switch source {
	case modules.ConfigFromDSC:
		parentGVK = gvk.DataScienceCluster
		parentNN = tc.DataScienceClusterNamespacedName
	case modules.ConfigFromDSCI:
		parentGVK = gvk.DSCInitialization
		parentNN = tc.DSCInitializationNamespacedName
		fieldPathPrefix = []string{"spec"}
	default:
		t.Fatalf("unsupported module config source %d", source)
	}

	registration := randomModuleRegistration(t, source)
	moduleName := registration.Handler.GetName()
	fieldName := moduleManagementField(registration, source)
	t.Logf("Enabling %s module %q for deletion coverage", configSourceName(source), moduleName)
	var mutate func(*unstructured.Unstructured) error
	if source == modules.ConfigFromDSC {
		mutate = selectComponentsTransform(string(operatorv1.Managed), []string{fieldName})
	} else {
		fieldPath := append(append([]string(nil), fieldPathPrefix...), fieldName, "managementState")
		mutate = func(obj *unstructured.Unstructured) error {
			return unstructured.SetNestedField(obj.Object, string(operatorv1.Managed), fieldPath...)
		}
	}
	tc.EventuallyResourcePatched(
		WithMinimalObject(parentGVK, parentNN),
		WithMutateFunc(mutate),
	)
}

func (tc *DeletionTestCtx) waitForOwnedModuleCRs(
	t *testing.T,
	source modules.ConfigSource,
	owner *unstructured.Unstructured,
) []discoveredModuleCR {
	t.Helper()

	var result []discoveredModuleCR
	tc.g.Eventually(func(g Gomega) {
		var err error
		result, err = tc.discoverOwnedModuleCRs(source, owner)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(result).NotTo(BeEmpty(), "expected at least one %s module CR owned by %s %s", configSourceName(source), owner.GetKind(), owner.GetName())
	}).WithTimeout(15 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

	return result
}

func (tc *DeletionTestCtx) discoverOwnedModuleCRs(
	source modules.ConfigSource,
	owner *unstructured.Unstructured,
) ([]discoveredModuleCR, error) {
	result := make([]discoveredModuleCR, 0)

	for _, registration := range modulebuiltin.Registrations() {
		if registration.ConfigSource != source {
			continue
		}

		moduleGVK := registration.Handler.GetGVK()
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   moduleGVK.Group,
			Version: moduleGVK.Version,
			Kind:    moduleGVK.Kind + "List",
		})
		if err := tc.Client().List(tc.Context(), list); err != nil {
			if meta.IsNoMatchError(err) || k8serr.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("list %s module CRs: %w", registration.Handler.GetName(), err)
		}

		for i := range list.Items {
			item := &list.Items[i]
			if !hasMatchingOwnerReference(item, owner) {
				continue
			}
			result = append(result, discoveredModuleCR{
				ModuleName: registration.Handler.GetName(),
				GVK:        moduleGVK,
				NamespacedName: types.NamespacedName{
					Namespace: item.GetNamespace(),
					Name:      item.GetName(),
				},
				UID: item.GetUID(),
			})
		}
	}

	return result, nil
}

func (tc *DeletionTestCtx) waitForPlatformOwnedBy(t *testing.T, owners ...*unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()

	var result *unstructured.Unstructured
	tc.g.Eventually(func(g Gomega) {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gvk.Platform.Group,
			Version: gvk.Platform.Version,
			Kind:    gvk.Platform.Kind + "List",
		})
		err := tc.Client().List(tc.Context(), list)
		g.Expect(err).NotTo(HaveOccurred())

		matches := make([]unstructured.Unstructured, 0, 1)
		for i := range list.Items {
			matchesAllOwners := true
			for _, owner := range owners {
				if !hasMatchingOwnerReference(&list.Items[i], owner) {
					matchesAllOwners = false
					break
				}
			}
			if matchesAllOwners {
				matches = append(matches, list.Items[i])
			}
		}

		g.Expect(matches).To(HaveLen(1), "expected exactly one Platform owned by the requested DSC/DSCI resources")
		result = matches[0].DeepCopy()
	}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

	return result
}

func (tc *DeletionTestCtx) addModuleDeletionHoldFinalizer(moduleCR discoveredModuleCR) {
	tc.EventuallyResourcePatched(
		WithMinimalObject(moduleCR.GVK, moduleCR.NamespacedName),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			if slices.Contains(obj.GetFinalizers(), moduleDeletionHoldFinalizer) {
				return nil
			}
			obj.SetFinalizers(append(obj.GetFinalizers(), moduleDeletionHoldFinalizer))
			return nil
		}),
		WithCondition(jq.Match(`.metadata.finalizers | index("%s") != null`, moduleDeletionHoldFinalizer)),
	)
}

func (tc *DeletionTestCtx) removeModuleDeletionHoldFinalizer(moduleCR discoveredModuleCR, ignoreNotFound bool) {
	opts := []ResourceOpts{
		WithMinimalObject(moduleCR.GVK, moduleCR.NamespacedName),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			finalizers := obj.GetFinalizers()
			filtered := make([]string, 0, len(finalizers))
			for _, finalizer := range finalizers {
				if finalizer != moduleDeletionHoldFinalizer {
					filtered = append(filtered, finalizer)
				}
			}
			obj.SetFinalizers(filtered)
			return nil
		}),
	}
	if ignoreNotFound {
		opts = append(opts, WithIgnoreNotFound(true))
	}
	tc.EventuallyResourcePatched(opts...)
}

func hasMatchingOwnerReference(obj metav1.Object, owner *unstructured.Unstructured) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() &&
			ref.APIVersion == owner.GetAPIVersion() &&
			ref.Kind == owner.GetKind() {
			return true
		}
	}
	return false
}

func namespacedNameFor(obj metav1.Object) types.NamespacedName {
	return types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}
}

func configSourceName(source modules.ConfigSource) string {
	switch source {
	case modules.ConfigFromDSC:
		return "DSC-backed"
	case modules.ConfigFromDSCI:
		return "DSCI-backed"
	default:
		return fmt.Sprintf("ConfigSource(%d)", source)
	}
}
