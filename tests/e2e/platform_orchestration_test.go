package e2e_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	ofapi "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/status"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/dag"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"

	. "github.com/onsi/gomega"
)

type DAGOrderingTestCtx struct {
	*TestContext

	contractReport *moduleContractReport
}

type moduleRegistrationNode struct {
	modulebuiltin.ModuleRegistration
}

func (n moduleRegistrationNode) GetName() string {
	return n.Handler.GetName()
}

func (n moduleRegistrationNode) GetRunlevel() dag.Runlevel {
	return n.Runlevel
}

func productionModuleBatches() ([][]moduleRegistrationNode, error) {
	graph := dag.NewGraph[moduleRegistrationNode]()
	for _, registration := range modulebuiltin.Registrations() {
		graph.Add(moduleRegistrationNode{ModuleRegistration: registration})
	}
	return graph.Resolve()
}

func managedDAGTestDSCComponentFields() []string {
	registrations := modulebuiltin.Registrations()
	fields := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		if registration.ConfigSource == modules.ConfigFromDSC {
			fields = append(fields, dscComponentField(registration.Handler.GetName()))
		}
	}
	slices.Sort(fields)
	return slices.Compact(fields)
}

// allDAGTestDSCComponentFields returns the component keys in the v3 DSC API.
// Keep this list independent of the v2 compatibility API: v2 uses separate
// feastoperator/modelregistry fields, while v3 exposes them as data/aiHub.
func allDAGTestDSCComponentFields() []string {
	return []string{
		"aiHub",
		"aigateway",
		"aipipelines",
		"dashboard",
		"data",
		"databaseservice",
		"kserve",
		"kueue",
		"mcplifecycleoperator",
		"mlflowoperator",
		"ogx",
		"ray",
		"sparkoperator",
		"trainer",
		"trustyai",
		"workbenches",
	}
}

func dscComponentField(moduleName string) string {
	switch moduleName {
	case "data":
		return "data"
	case "aihub":
		return "aiHub"
	default:
		return moduleName
	}
}

const (
	dagQuotaName                    = "dag-test-restrictive-quota"
	dagOperatorEnvSnapshotConfigMap = "e2e-dag-operator-env-snapshot"
	dagTestVersion                  = "99.0.0-dag-test"
	dagRunlevelTimeoutEnv           = "ODH_DAG_RUNLEVEL_TIMEOUT"
	dagE2ERunlevelTimeout           = 5 * time.Minute
)

// platformOrchestrationTestSuite runs the OpenShift platform provisioning,
// runlevel gating, convergence, and registered-module contract suite.
// xKS contract coverage is intentionally deferred to a separate future suite.
func platformOrchestrationTestSuite(t *testing.T) {
	t.Helper()

	skipUnless(t, Tier2)

	tc, err := NewTestContext(t)
	require.NoError(t, err, "Failed to initialize test context")

	ctx := DAGOrderingTestCtx{
		TestContext:    tc,
		contractReport: &moduleContractReport{},
	}

	if tc.IsXKS() {
		t.Skip("xKS module contract coverage is out of scope for this suite")
	}

	ctx.runOpenShiftTestCases(t)
}

// runOpenShiftTestCases exercises DAG runlevel gating on OpenShift
// clusters, where component enablement goes through the
// DataScienceCluster (DSC).
func (tc *DAGOrderingTestCtx) runOpenShiftTestCases(t *testing.T) {
	t.Helper()

	tc.ensureOperatorPrerequisites(t)

	envSnapshot := tc.restorePersistedOperatorEnvSnapshot(t)
	if envSnapshot == nil {
		envSnapshot = tc.snapshotOperatorEnvVars(t, "RHAI_VERSION", "CI", dagRunlevelTimeoutEnv)
	}
	tc.persistOperatorEnvSnapshot(t, envSnapshot)
	tc.resetStaleDagVersionOverride(t, envSnapshot)

	t.Log("Ensuring clean slate: setting all components to Removed")
	tc.setAllRemoved(t)
	tc.setOperatorEnvVars(t, corev1.EnvVar{Name: dagRunlevelTimeoutEnv, Value: dagE2ERunlevelTimeout.String()})

	t.Cleanup(func() {
		t.Log("Cleanup: setting all components to Removed, deleting quota, and restoring operator env vars")
		tc.setAllRemoved(t)
		tc.deleteDAGQuota()
		tc.restoreOperatorEnvVars(t, envSnapshot)
	})

	// Gate tests run first while the clean slate is Removed.
	RunTestCases(t, []TestCase{
		{"Validate in-tree gates block and unblock provisioning", tc.ValidateInTreeGates},
		{"Validate admin ack gates block and unblock provisioning", tc.ValidateAdminAckGates},
	})

	RunTestCases(t, []TestCase{
		{"Validate platform convergence and module contracts", tc.ValidatePlatformConvergenceAndModuleContracts},
		{"Report module contract failures", tc.ReportModuleContractFailures},
	})

	// Upgrade gates and runlevel progression are isolated from the initial
	// platform convergence and module-contract checks, while sharing setup and
	// cleanup through this parent suite.
	t.Run("DAG_Ordering_Upgrade_E2E_Tests", tc.ValidateDAGOrderingUpgrade)

	// Cleanup
	RunTestCases(t, []TestCase{
		{"Validate DAG cleanup", tc.ValidateDAGCleanup},
	})
}

// ValidatePlatformConvergenceAndModuleContracts validates initial deployment,
// DSC/Platform convergence, and every registered module contract. Upgrade ordering
// is exercised separately by ValidateDAGOrderingUpgrade.
func (tc *DAGOrderingTestCtx) ValidatePlatformConvergenceAndModuleContracts(t *testing.T) {
	t.Helper()

	batches, err := productionModuleBatches()
	require.NoError(t, err, "resolve production module DAG")
	require.Greater(t, len(batches), 1, "module DAG should contain multiple runlevels")

	tc.deleteDAGQuota()
	t.Log("Enabling all registered DSC modules and DSCI Monitoring")
	tc.setDSCIMonitoringState(t, operatorv1.Managed)
	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithMutateFunc(allComponentsManagedTransform()),
	)
	t.Log("Waiting for DataScienceCluster Ready before checking module contracts")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCondition(And(
			jq.Match(`.status.observedGeneration == .metadata.generation`),
			jq.Match(
				`any(.status.conditions[]; .type == "%s" and .status == "%s" and .observedGeneration == .metadata.generation)`,
				status.ConditionTypeReady,
				metav1.ConditionTrue,
			),
			jq.Match(
				`any(.status.conditions[]; .type == "%s" and .status == "%s" and .observedGeneration == .metadata.generation)`,
				status.ConditionTypeModulesReady,
				metav1.ConditionTrue,
			),
		)),
		WithEventuallyTimeout(15*time.Minute),
		WithEventuallyPollingInterval(15*time.Second),
		WithCustomErrorMsg("DataScienceCluster should be Ready with all modules ready before module contract checks"),
	)
	t.Log("DataScienceCluster is Ready with all modules ready; checking module contract entries")
	tc.ValidateRegisteredModuleContracts(t)

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, tc.PlatformNamespacedName),
		WithCondition(And(
			jq.Match(
				`any(.status.conditions[]; .type == "%s" and .status == "%s")`,
				status.ConditionTypeProvisioningProgress, metav1.ConditionTrue,
			),
			jq.Match(
				`any(.status.conditions[]; .type == "%s" and .status == "%s")`,
				status.ConditionTypeModulesReady, metav1.ConditionTrue,
			),
		)),
		WithEventuallyTimeout(15*time.Minute),
		WithEventuallyPollingInterval(15*time.Second),
	)
}

func (tc *DAGOrderingTestCtx) waitForBatchUpgradeBeforeDeployment(
	t *testing.T,
	batch []moduleRegistrationNode,
	gatedDeployment types.NamespacedName,
) {
	t.Helper()

	deadline := time.Now().Add(dagE2ERunlevelTimeout - 30*time.Second)
	for time.Now().Before(deadline) {
		acknowledged := true
		for _, node := range batch {
			moduleStatus, err := node.Handler.GetModuleStatus(tc.Context(), tc.Client())
			if err != nil || moduleStatus.ReleaseVersion != dagTestVersion {
				acknowledged = false
				break
			}
		}
		if acknowledged {
			return
		}

		deployment := &unstructured.Unstructured{}
		deployment.SetGroupVersionKind(gvk.Deployment)
		err := tc.Client().Get(tc.Context(), gatedDeployment, deployment)
		if !k8serr.IsNotFound(err) {
			require.NoError(t, err, "get gated module operator Deployment")
			t.Fatalf("later-runlevel Deployment %s was recreated before runlevel 20 acknowledged platform version %s",
				gatedDeployment.Name, dagTestVersion)
		}
		time.Sleep(5 * time.Second)
	}

	t.Fatalf("runlevel 20 did not acknowledge platform version %s before the %s DAG timeout",
		dagTestVersion, dagE2ERunlevelTimeout)
}

func (tc *DAGOrderingTestCtx) deleteModuleOperatorPods(t *testing.T, batch []moduleRegistrationNode) {
	t.Helper()

	for _, node := range batch {
		deployment := tc.FetchResource(
			WithMinimalObject(gvk.Deployment, types.NamespacedName{
				Namespace: tc.AppsNamespace,
				Name:      moduleDeploymentName(node.Handler),
			}),
		)
		if deployment == nil {
			continue
		}
		selector, _, err := unstructured.NestedStringMap(deployment.Object, "spec", "selector", "matchLabels")
		require.NoError(t, err, "read selector for module %s", node.Handler.GetName())
		if len(selector) == 0 {
			continue
		}
		pods := tc.FetchResources(
			WithMinimalObject(gvk.Pod, types.NamespacedName{Namespace: deployment.GetNamespace()}),
			WithListOptions(&client.ListOptions{
				Namespace:     deployment.GetNamespace(),
				LabelSelector: labels.SelectorFromSet(selector),
			}),
		)
		for i := range pods {
			tc.DeleteResource(
				WithMinimalObject(gvk.Pod, types.NamespacedName{
					Namespace: pods[i].GetNamespace(),
					Name:      pods[i].GetName(),
				}),
				WithWaitForDeletion(true),
			)
		}
	}
}

func (tc *DAGOrderingTestCtx) moduleOperatorDeploymentNames() (map[string]struct{}, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.Deployment.GroupVersion().WithKind("DeploymentList"))
	if err := tc.Client().List(tc.Context(), list); err != nil {
		return nil, err
	}

	names := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		names[list.Items[i].GetName()] = struct{}{}
	}
	return names, nil
}

type operatorPrerequisite struct {
	namespacedName types.NamespacedName
	channel        string
	global         bool
}

func (tc *DAGOrderingTestCtx) ensureOperatorPrerequisites(t *testing.T) {
	t.Helper()

	prerequisites := []operatorPrerequisite{
		{
			namespacedName: types.NamespacedName{Name: certManagerOpName, Namespace: certManagerOpNamespace},
			channel:        certManagerOpChannel,
			global:         true,
		},
		{
			namespacedName: types.NamespacedName{Name: jobSetOpName, Namespace: jobSetOpNamespace},
			channel:        jobSetOpChannel,
		},
		{
			namespacedName: types.NamespacedName{Name: kuadrantOpName, Namespace: kuadrantNamespace},
			channel:        defaultOperatorChannel,
			global:         true,
		},
	}

	tc.ensureOperatorsInstalled(t, prerequisites)
	tc.EnsureCRDEstablished("issuers.cert-manager.io")
	tc.EnsureCRDEstablished("certificates.cert-manager.io")
	tc.EventuallyResourceCreatedOrUpdated(
		WithObjectToCreate(CreateJobSetOperator()),
		WithCondition(jq.Match(`.status.conditions[] | select(.type == "Available") | .status == "True"`)),
		WithCustomErrorMsg("JobSetOperator cluster resource should become Available"),
	)
}

func (tc *DAGOrderingTestCtx) ensureOperatorsInstalled(t *testing.T, prerequisites []operatorPrerequisite) {
	t.Helper()

	for _, prerequisite := range prerequisites {
		t.Logf("Ensuring prerequisite operator %s is installed in namespace %s",
			prerequisite.namespacedName.Name, prerequisite.namespacedName.Namespace)
		if prerequisite.global {
			tc.EnsureOperatorInstalledWithGlobalOperatorGroupAndChannel(
				prerequisite.namespacedName,
				prerequisite.channel,
			)
			continue
		}
		tc.EnsureOperatorInstalledWithLocalOperatorGroupAndChannel(
			prerequisite.namespacedName,
			prerequisite.channel,
		)
	}
}

func (tc *DAGOrderingTestCtx) ValidateRegisteredModuleContracts(t *testing.T) {
	t.Helper()

	if tc.IsXKS() {
		t.Log("Skipping the OpenShift module contract on xKS")
		return
	}

	batches, err := productionModuleBatches()
	if err != nil {
		t.Errorf("resolve production module DAG: %v", err)
		return
	}

	for _, batch := range batches {
		if len(batch) == 0 {
			continue
		}
		runlevel := batch[0].Runlevel
		t.Run(fmt.Sprintf("Runlevel %s module contracts", runlevel), func(t *testing.T) {
			for _, node := range batch {
				registration := node.ModuleRegistration
				t.Run(registration.Handler.GetName(), func(t *testing.T) {
					ctx := newModuleContractTestCtx(t, tc.TestContext, registration, tc.contractReport)
					ctx.ValidateModuleContract(t)
				})
			}
		})
	}
}

// ReportModuleContractFailures emits the aggregate module contract findings in
// a dedicated stage so they are not attributed to the last module subtest.
func (tc *DAGOrderingTestCtx) ReportModuleContractFailures(t *testing.T) {
	t.Helper()
	tc.contractReport.log(t)
}

// ValidateDAGCleanup sets all components to Removed, waits for the DSC
// to reach Ready, and verifies that all component CRs are cleaned up
// with no orphans.
//
// NOTE: Reverse-batch cleanup ordering (ReverseBatches) is implemented
// in the modules controller path but not in the current mixed
// component/module environment where the DSC GC action deletes all
// stale component CRs in a single pass. Once the migration to modules
// is complete, this test should be extended to assert reverse-batch
// deletion ordering.
func (tc *DAGOrderingTestCtx) ValidateDAGCleanup(t *testing.T) {
	t.Helper()

	tc.setAllRemoved(t)

	t.Log("Verifying all component CRs are cleaned up (no orphans)")
	tc.ensureAllRemovedComponentsGone(t)
}

// --- helpers ---

func allComponentsManagedTransform() func(*unstructured.Unstructured) error {
	return selectComponentsTransform("Managed", managedDAGTestDSCComponentFields())
}

func allComponentsRemovedTransform() func(*unstructured.Unstructured) error {
	return selectComponentsTransform("Removed", allDAGTestDSCComponentFields())
}

func selectComponentsTransform(state string, fields []string) func(*unstructured.Unstructured) error {
	return func(obj *unstructured.Unstructured) error {
		for _, field := range fields {
			componentPath := []string{"spec", "components", field}
			switch field {
			case "dashboard":
				componentPath = append(componentPath, "standard")
			case "data":
				// The v3 Data component owns both legacy Data Registry and
				// Feature Store configuration. Set both so cleanup cannot
				// leave one of the subcomponents enabled.
				for _, subcomponent := range []string{"featureStore", "dataRegistry"} {
					if err := unstructured.SetNestedField(
						obj.Object, state, append(componentPath, subcomponent, "managementState")...,
					); err != nil {
						return err
					}
				}
				continue
			}
			if err := unstructured.SetNestedField(
				obj.Object, state, append(componentPath, "managementState")...,
			); err != nil {
				return err
			}
		}
		return nil
	}
}

// setDSCIMonitoringState patches the DSCI to set monitoring management state.
func (tc *DAGOrderingTestCtx) setDSCIMonitoringState(t *testing.T, state operatorv1.ManagementState) {
	t.Helper()
	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.DSCInitialization, tc.DSCInitializationNamespacedName),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			return unstructured.SetNestedField(obj.Object, string(state), "spec", "monitoring", "managementState")
		}),
	)
}

// setAllRemoved is a reusable helper that sets all components to Removed,
// waits for Ready=True, and verifies all component CRs are deleted.
func (tc *DAGOrderingTestCtx) setAllRemoved(t *testing.T) {
	t.Helper()

	tc.setDSCIMonitoringState(t, operatorv1.Removed)

	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithMutateFunc(allComponentsRemovedTransform()),
	)

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .status == "%s")`,
			status.ConditionTypeReady, metav1.ConditionTrue,
		)),
		WithEventuallyTimeout(10*time.Minute),
		WithEventuallyPollingInterval(15*time.Second),
	)

	tc.ensureAllRemovedComponentsGone(t)
	tc.ensureAllRemovedOperatorDeploymentsGone(t)
}

// ensureAllRemovedComponentsGone verifies that every production module CR is
// gone after its DSC/DSCI source is set to Removed. Kueue is intentionally not
// included because it remains an in-tree component rather than a module.
func (tc *DAGOrderingTestCtx) ensureAllRemovedComponentsGone(t *testing.T) {
	t.Helper()

	for _, registration := range modulebuiltin.Registrations() {
		t.Logf("Waiting for all %s module CRs to be removed", registration.Handler.GetName())
		tc.EnsureResourcesGone(
			WithMinimalObject(registration.Handler.GetGVK(), types.NamespacedName{}),
			WithEventuallyTimeout(5*time.Minute),
			WithEventuallyPollingInterval(10*time.Second),
		)
	}
}

func (tc *DAGOrderingTestCtx) ensureAllRemovedOperatorDeploymentsGone(t *testing.T) {
	t.Helper()

	for _, registration := range modulebuiltin.Registrations() {
		name := moduleDeploymentName(registration.Handler)
		tc.g.Eventually(func(g Gomega) {
			deployed, err := tc.moduleOperatorDeploymentNames()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deployed).NotTo(HaveKey(name))
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(Succeed(),
			"module operator Deployment %s should be removed", name)
	}
}

func (tc *DAGOrderingTestCtx) createDAGQuota() {
	quota := &corev1.ResourceQuota{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gvk.ResourceQuota.Version,
			Kind:       gvk.ResourceQuota.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dagQuotaName,
			Namespace: tc.AppsNamespace,
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods: resource.MustParse("0"),
			},
		},
	}

	tc.EventuallyResourceCreated(WithObjectToCreate(quota))

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.ResourceQuota, types.NamespacedName{
			Namespace: tc.AppsNamespace,
			Name:      dagQuotaName,
		}),
		WithCondition(jq.Match(`.status.hard != null`)),
		WithEventuallyTimeout(tc.TestTimeouts.mediumEventuallyTimeout),
	)
}

// getDeployedVersion reads the operator release version from the DSC
// status on the cluster. This avoids relying on cluster.GetRelease()
// which returns 0.0.0 when CI=true short-circuits version detection
// in the e2e test binary.
func (tc *DAGOrderingTestCtx) getDeployedVersion(t *testing.T) string {
	t.Helper()

	dsc := tc.FetchResource(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
	)
	require.NotNil(t, dsc, "DSC must exist to read deployed version")

	ver, _, _ := unstructured.NestedString(dsc.Object, "status", "release", "version")
	require.NotEmpty(t, ver, "DSC .status.release.version must be populated")

	t.Logf("Using deployed operator version from DSC status: %s", ver)

	return ver
}

func (tc *DAGOrderingTestCtx) deleteDAGQuota() {
	tc.DeleteResource(
		WithMinimalObject(
			gvk.ResourceQuota,
			types.NamespacedName{Namespace: tc.AppsNamespace, Name: dagQuotaName},
		),
		WithIgnoreNotFound(true),
		WithWaitForDeletion(true),
	)
}

// setOperatorEnvVars sets environment variables on the operator, triggering
// a single pod restart. For OLM installs it patches the CSV (OLM propagates to
// the Deployment). For non-OLM installs it patches the Deployment directly.
func (tc *DAGOrderingTestCtx) persistOperatorEnvSnapshot(t *testing.T, snapshot operatorEnvSnapshot) {
	t.Helper()

	data := make(map[string]string, len(snapshot))
	for name, value := range snapshot {
		encoded, err := json.Marshal(value)
		require.NoError(t, err, "encode operator environment snapshot %s", name)
		data[name] = string(encoded)
	}

	key := types.NamespacedName{
		Namespace: tc.OperatorNamespace,
		Name:      dagOperatorEnvSnapshotConfigMap,
	}
	configMap := &corev1.ConfigMap{}
	err := tc.Client().Get(tc.Context(), key, configMap)
	switch {
	case k8serr.IsNotFound(err):
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: tc.OperatorNamespace,
				Name:      dagOperatorEnvSnapshotConfigMap,
				Labels: map[string]string{
					"opendatahub.io/created-by-e2e-tests": "true",
				},
			},
			Data: data,
		}
		require.NoError(t, tc.Client().Create(tc.Context(), configMap), "persist operator environment snapshot")
	case err != nil:
		require.NoError(t, err, "read operator environment snapshot")
	default:
		configMap.Data = data
		require.NoError(t, tc.Client().Update(tc.Context(), configMap), "update operator environment snapshot")
	}
	t.Logf("Persisted operator environment snapshot in %s/%s", tc.OperatorNamespace, dagOperatorEnvSnapshotConfigMap)
}

func (tc *DAGOrderingTestCtx) restorePersistedOperatorEnvSnapshot(t *testing.T) operatorEnvSnapshot {
	t.Helper()

	configMap := &corev1.ConfigMap{}
	err := tc.Client().Get(tc.Context(), types.NamespacedName{
		Namespace: tc.OperatorNamespace,
		Name:      dagOperatorEnvSnapshotConfigMap,
	}, configMap)
	if k8serr.IsNotFound(err) {
		return nil
	}
	require.NoError(t, err, "read persisted operator environment snapshot")

	snapshot := make(operatorEnvSnapshot, len(configMap.Data))
	for name, encoded := range configMap.Data {
		var value *corev1.EnvVar
		require.NoError(t, json.Unmarshal([]byte(encoded), &value), "decode operator environment snapshot %s", name)
		snapshot[name] = value
	}
	t.Logf("Restoring persisted operator environment snapshot from %s/%s", tc.OperatorNamespace, dagOperatorEnvSnapshotConfigMap)
	tc.restoreOperatorEnvVars(t, snapshot)
	return snapshot
}

func (tc *DAGOrderingTestCtx) deleteOperatorEnvSnapshot(t *testing.T) {
	t.Helper()

	err := tc.Client().Delete(tc.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: tc.OperatorNamespace,
		Name:      dagOperatorEnvSnapshotConfigMap,
	}})
	if err != nil && !k8serr.IsNotFound(err) {
		t.Errorf("failed to delete operator environment snapshot: %v", err)
		return
	}
	t.Logf("Deleted operator environment snapshot %s/%s", tc.OperatorNamespace, dagOperatorEnvSnapshotConfigMap)
}

func (tc *DAGOrderingTestCtx) resetStaleDagVersionOverride(t *testing.T, snapshot operatorEnvSnapshot) {
	t.Helper()

	version := snapshot["RHAI_VERSION"]
	if version == nil || version.Value != dagTestVersion {
		return
	}

	t.Logf("Resetting stale RHAI_VERSION=%q before DAG cleanup; retaining snapshot for restoration", version.Value)
	if tc.findSubscription(t) != nil {
		if !tc.patchSubscriptionEnvVarRemovals(t, []string{"RHAI_VERSION"}) {
			t.Log("Subscription disappeared while resetting stale RHAI_VERSION; falling back to Deployment patch")
			tc.patchDeploymentEnvVars(t, nil, []string{"RHAI_VERSION"})
		}
	} else {
		tc.patchDeploymentEnvVars(t, nil, []string{"RHAI_VERSION"})
	}
	tc.waitForOperatorReady(t)
}

func (tc *DAGOrderingTestCtx) setOperatorEnvVars(t *testing.T, envs ...corev1.EnvVar) {
	t.Helper()

	if tc.patchSubscriptionEnvVars(t, envs) {
		t.Logf("Patched Subscription to set env vars: %v", envVarNames(envs))
	} else {
		t.Logf("No OLM Subscription found, patching Deployment directly")
		tc.patchDeploymentEnvVars(t, envs, nil)
	}

	tc.waitForOperatorReady(t)
}

type operatorEnvSnapshot map[string]*corev1.EnvVar

func (tc *DAGOrderingTestCtx) snapshotOperatorEnvVars(t *testing.T, names ...string) operatorEnvSnapshot {
	t.Helper()

	var envs []corev1.EnvVar
	if sub := tc.findSubscription(t); sub != nil {
		if sub.Spec.Config != nil {
			envs = sub.Spec.Config.Env
		}
	} else {
		deployment := &appsv1.Deployment{}
		err := tc.Client().Get(tc.Context(), tc.operatorDeploymentNN(), deployment)
		require.NoError(t, err, "get operator Deployment")
		require.NotEmpty(t, deployment.Spec.Template.Spec.Containers, "operator Deployment has no containers")
		envs = deployment.Spec.Template.Spec.Containers[0].Env
	}

	snapshot := make(operatorEnvSnapshot, len(names))
	for _, name := range names {
		for i := range envs {
			if envs[i].Name == name {
				value := envs[i]
				snapshot[name] = &value
				break
			}
		}
	}
	return snapshot
}

func (tc *DAGOrderingTestCtx) restoreOperatorEnvVars(t *testing.T, snapshot operatorEnvSnapshot) {
	t.Helper()

	toSet := make([]corev1.EnvVar, 0, len(snapshot))
	toRemove := make([]string, 0, len(snapshot))
	for name, value := range snapshot {
		if value == nil {
			toRemove = append(toRemove, name)
			continue
		}
		toSet = append(toSet, *value)
	}

	if tc.findSubscription(t) != nil {
		tc.patchSubscriptionEnvVarRemovals(t, toRemove)
		tc.patchSubscriptionEnvVars(t, toSet)
		t.Logf("Restored Subscription env vars: %v", envVarNames(toSet))
	} else {
		tc.patchDeploymentEnvVars(t, toSet, toRemove)
		t.Logf("Restored Deployment env vars: %v", envVarNames(toSet))
	}
	tc.waitForOperatorReady(t)
	tc.deleteOperatorEnvSnapshot(t)
}

// patchSubscriptionEnvVars patches the operator Subscription to set env vars
// via spec.config.env. OLM propagates this to the CSV and Deployment.
// Returns false if no Subscription is found (non-OLM install).
func (tc *DAGOrderingTestCtx) patchSubscriptionEnvVars(t *testing.T, envs []corev1.EnvVar) bool {
	t.Helper()

	sub := tc.findSubscription(t)
	if sub == nil {
		return false
	}

	if sub.Spec.Config == nil {
		sub.Spec.Config = &ofapi.SubscriptionConfig{}
	}

	for _, env := range envs {
		sub.Spec.Config.Env = slices.DeleteFunc(sub.Spec.Config.Env, func(e corev1.EnvVar) bool {
			return e.Name == env.Name
		})
		sub.Spec.Config.Env = append(sub.Spec.Config.Env, env)
	}

	err := tc.Client().Update(tc.Context(), sub)
	require.NoError(t, err, "Failed to update Subscription %s", sub.Name)

	return true
}

// patchSubscriptionEnvVarRemovals patches the operator Subscription to remove
// env vars. Returns false if no Subscription is found (non-OLM install).
func (tc *DAGOrderingTestCtx) patchSubscriptionEnvVarRemovals(t *testing.T, envNames []string) bool {
	t.Helper()

	sub := tc.findSubscription(t)
	if sub == nil {
		return false
	}

	if sub.Spec.Config == nil {
		return true
	}

	for _, name := range envNames {
		sub.Spec.Config.Env = slices.DeleteFunc(sub.Spec.Config.Env, func(e corev1.EnvVar) bool {
			return e.Name == name
		})
	}

	err := tc.Client().Update(tc.Context(), sub)
	require.NoError(t, err, "Failed to update Subscription %s", sub.Name)

	return true
}

// findSubscription returns the operator Subscription, or nil if not found.
func (tc *DAGOrderingTestCtx) findSubscription(t *testing.T) *ofapi.Subscription {
	t.Helper()

	subList := &ofapi.SubscriptionList{}
	err := tc.Client().List(tc.Context(), subList, client.InNamespace(tc.OperatorNamespace))
	if err != nil {
		if k8serr.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		require.NoError(t, err, "failed to list Subscriptions")
	}

	subIdx := slices.IndexFunc(subList.Items, func(sub ofapi.Subscription) bool {
		return strings.Contains(sub.Name, "opendatahub") || strings.Contains(sub.Name, "rhods")
	})
	if subIdx == -1 {
		return nil
	}

	return &subList.Items[subIdx]
}

// patchDeploymentEnvVars patches the operator Deployment to add/remove env vars
// in a single mutation. toSet are added/updated, toRemove are removed by name.
func (tc *DAGOrderingTestCtx) patchDeploymentEnvVars(t *testing.T, toSet []corev1.EnvVar, toRemove []string) {
	t.Helper()

	removeSet := make(map[string]bool, len(toRemove))
	for _, name := range toRemove {
		removeSet[name] = true
	}
	for _, env := range toSet {
		removeSet[env.Name] = true
	}

	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.Deployment, tc.operatorDeploymentNN()),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			if len(containers) == 0 {
				return errors.New("operator Deployment has no containers")
			}
			container, ok := containers[0].(map[string]any)
			if !ok {
				return errors.New("first container is not a map[string]any")
			}
			env, _ := container["env"].([]any)

			var filtered []any
			for _, e := range env {
				if entry, ok := e.(map[string]any); ok {
					if name, _ := entry["name"].(string); removeSet[name] {
						continue
					}
				}
				filtered = append(filtered, e)
			}

			for _, ev := range toSet {
				filtered = append(filtered, map[string]any{"name": ev.Name, "value": ev.Value})
			}

			container["env"] = filtered
			containers[0] = container
			return unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers")
		}),
	)
}

func envVarNames(envs []corev1.EnvVar) []string {
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	return names
}

// waitForOperatorReady waits for the operator deployment to have all replicas ready.
func (tc *DAGOrderingTestCtx) waitForOperatorReady(t *testing.T) {
	t.Helper()

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, tc.operatorDeploymentNN()),
		WithCondition(jq.Match(`.status.readyReplicas == .status.replicas and .status.updatedReplicas == .status.replicas`)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
}

func (tc *DAGOrderingTestCtx) operatorDeploymentName() string {
	if cluster.GetRelease().Name == cluster.SelfManagedRhoai || cluster.GetRelease().Name == cluster.ManagedRhoai {
		return controllerDeploymentRhoai
	}
	return controllerDeploymentODH
}

func (tc *DAGOrderingTestCtx) operatorDeploymentNN() types.NamespacedName {
	return types.NamespacedName{Name: tc.operatorDeploymentName(), Namespace: tc.OperatorNamespace}
}
