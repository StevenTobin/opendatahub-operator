package e2e_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/status"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/gates"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"

	. "github.com/onsi/gomega"
)

// ValidateDAGOrderingUpgrade proves that a platform-version upgrade remains
// blocked at a later runlevel until the earlier runlevel acknowledges it.
func (tc *DAGOrderingTestCtx) ValidateDAGOrderingUpgrade(t *testing.T) {
	t.Helper()

	batches, err := productionModuleBatches()
	if err != nil {
		t.Fatalf("resolve production module DAG: %v", err)
	}
	if len(batches) <= 1 {
		t.Fatalf("module DAG should contain multiple runlevels")
	}

	laterDeployments := make([][]types.NamespacedName, len(batches))
	for batchIndex := 1; batchIndex < len(batches); batchIndex++ {
		for _, node := range batches[batchIndex] {
			laterDeployments[batchIndex] = append(laterDeployments[batchIndex], types.NamespacedName{
				Namespace: tc.AppsNamespace,
				Name:      moduleDeploymentName(node.Handler),
			})
		}
	}

	tc.createDAGQuota()
	t.Cleanup(tc.deleteDAGQuota)
	tc.deleteModuleOperatorPods(t, batches[0])
	t.Logf("Ensuring the current platform version differs from %s before triggering the DAG upgrade", dagTestVersion)
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCondition(jq.Match(
			`.status.release.version != null and .status.release.version != "" and .status.release.version != "%s"`,
			dagTestVersion,
		)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(5*time.Second),
		WithCustomErrorMsg("DSC should report a baseline version before the DAG upgrade test"),
	)
	tc.setOperatorEnvVars(t,
		corev1.EnvVar{Name: "CI", Value: "true"},
		corev1.EnvVar{Name: "RHAI_VERSION", Value: dagTestVersion},
	)

	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, tc.PlatformNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .status == "%s" and .reason == "%s")`,
			status.ConditionTypeProvisioningProgress,
			metav1.ConditionFalse,
			status.AwaitingReadinessReason,
		)),
		WithEventuallyTimeout(3*time.Minute),
		WithEventuallyPollingInterval(5*time.Second),
		WithCustomErrorMsg("Platform should block later upgrade runlevels while runlevel 20 reports the old version"),
	)

	for batchIndex := 1; batchIndex < len(batches); batchIndex++ {
		for _, deployment := range laterDeployments[batchIndex] {
			tc.DeleteResource(
				WithMinimalObject(gvk.Deployment, deployment),
				WithWaitForDeletion(true),
			)
		}
	}

	// Keep checking every later runlevel for a bounded negative window before
	// releasing the quota. This proves that blocking the first batch gates the
	// complete remainder of the DAG, rather than only one representative Deployment.
	tc.requireDeploymentsAbsent(t, laterDeployments[1:]...)

	t.Log("Removing quota so the first runlevel can acknowledge the new platform version")
	tc.deleteDAGQuota()
	tc.waitForAdjacentBatchUpgradeOrdering(t, batches, laterDeployments)

	for batchIndex := 1; batchIndex < len(batches); batchIndex++ {
		for _, deployment := range laterDeployments[batchIndex] {
			tc.EnsureResourceExists(
				WithMinimalObject(gvk.Deployment, deployment),
				WithCondition(jq.Match(`any(.status.conditions[]; .type == "Available" and .status == "True")`)),
				WithEventuallyTimeout(15*time.Minute),
				WithEventuallyPollingInterval(15*time.Second),
			)
		}
	}
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

func (tc *DAGOrderingTestCtx) requireDeploymentsAbsent(t *testing.T, deploymentBatches ...[]types.NamespacedName) {
	t.Helper()

	tc.g.Consistently(func(g Gomega) {
		for batchIndex, deployments := range deploymentBatches {
			for _, deploymentName := range deployments {
				deployment := &unstructured.Unstructured{}
				deployment.SetGroupVersionKind(gvk.Deployment)
				err := tc.Client().Get(tc.Context(), deploymentName, deployment)
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(),
					"module operator Deployment %s from later batch %d should remain absent while the first batch is blocked",
					deploymentName.Name, batchIndex+2)
			}
		}
	}).WithTimeout(30 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
}

func (tc *DAGOrderingTestCtx) waitForAdjacentBatchUpgradeOrdering(
	t *testing.T,
	batches [][]moduleRegistrationNode,
	deployments [][]types.NamespacedName,
) {
	t.Helper()

	deadline := time.Now().Add(dagE2ERunlevelTimeout - 30*time.Second)
	for time.Now().Before(deadline) {
		allBoundariesReleased := true
		for batchIndex := range len(batches) - 1 {
			acknowledged := true
			for _, node := range batches[batchIndex] {
				moduleStatus, err := node.Handler.GetModuleStatus(tc.Context(), tc.Client())
				if err != nil || moduleStatus.ReleaseVersion != dagTestVersion {
					acknowledged = false
					break
				}
			}
			if acknowledged {
				continue
			}

			allBoundariesReleased = false
			for _, deploymentName := range deployments[batchIndex+1] {
				deployment := &unstructured.Unstructured{}
				deployment.SetGroupVersionKind(gvk.Deployment)
				err := tc.Client().Get(tc.Context(), deploymentName, deployment)
				if !k8serr.IsNotFound(err) {
					require.NoError(t, err, "get gated module operator Deployment %s", deploymentName.Name)
					t.Fatalf("batch %d module operator Deployment %s was recreated before adjacent batch %d acknowledged platform version %s",
						batchIndex+2, deploymentName.Name, batchIndex+1, dagTestVersion)
				}
			}
		}
		if allBoundariesReleased {
			return
		}
		time.Sleep(5 * time.Second)
	}

	t.Fatalf("not all adjacent runlevel boundaries were released for platform version %s before the %s DAG timeout",
		dagTestVersion, dagE2ERunlevelTimeout)
}

// ValidateInTreeGates verifies that gate entries compiled into the
// operator binary (from pkg/controller/gates/resources/gates.yaml)
// block provisioning and can be acknowledged. The test self-discovers
// in-tree entries for the current version and skips if none exist.
func (tc *DAGOrderingTestCtx) ValidateInTreeGates(t *testing.T) {
	t.Helper()

	operatorVersion := tc.getDeployedVersion(t)

	intreeGates, err := gates.LoadInTreeGates(operatorVersion)
	require.NoError(t, err, "Failed to load in-tree gates")

	if len(intreeGates) == 0 {
		t.Skipf("No in-tree gates for version %s, skipping", operatorVersion)
	}

	t.Logf("Found %d in-tree gate(s) for version %s", len(intreeGates), operatorVersion)

	// Suite-level setup (or prior test) leaves all components Removed.

	gateKeys := make([]string, 0, len(intreeGates))
	for k := range intreeGates {
		gateKeys = append(gateKeys, k)
		t.Logf("  gate: %s = %q", k, intreeGates[k])
	}

	tc.clearStaleAcks(t, gateKeys...)

	t.Log("Enabling dashboard and aigateway to trigger a reconcile (in-tree gates should be discovered)")
	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithMutateFunc(selectComponentsTransform("Managed", []string{"dashboard", "aigateway"})),
	)

	t.Log("Waiting for ProvisioningProgress=False with reason AdminAckRequired")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .status == "%s" and .reason == "%s")`,
			status.ConditionTypeProvisioningProgress, metav1.ConditionFalse, status.AdminAckRequiredReason,
		)),
		WithEventuallyTimeout(3*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
	t.Log("Confirmed: provisioning blocked by in-tree gates (components and modules)")

	t.Log("Verifying gate descriptions were written to odh-upgrade-acks")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      gates.AcksConfigMap,
		}),
		WithCondition(jq.Match(`.data["%s"] != null and .data["%s"] != "true"`, gateKeys[0], gateKeys[0])),
		WithEventuallyTimeout(30*time.Second),
		WithEventuallyPollingInterval(5*time.Second),
	)
	t.Log("Confirmed: in-tree gate descriptions present in odh-upgrade-acks ConfigMap")

	t.Log("Acknowledging all in-tree gates")
	for _, key := range gateKeys {
		tc.patchAcksConfigMap(t, key, "true")
	}

	t.Log("Waiting for provisioning to resume")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .reason != "%s")`,
			status.ConditionTypeProvisioningProgress, status.AdminAckRequiredReason,
		)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
	t.Log("Confirmed: provisioning resumed after in-tree gates acknowledged")

	t.Log("Cleaning up: setting all components to Removed")
	tc.setAllRemoved(t)
}

// ValidateAdminAckGates creates labeled source ConfigMaps (simulating
// gate declarations from component/module charts), enables a component,
// and verifies that the operator discovers them, writes descriptions
// to odh-upgrade-acks, and blocks provisioning with AdminAckRequired.
// It then partially acks (verifying blocking continues) and fully acks
// (verifying provisioning resumes).
func (tc *DAGOrderingTestCtx) ValidateAdminAckGates(t *testing.T) {
	t.Helper()

	// Prior test (InTreeGates) leaves all components Removed.

	operatorVersion := tc.getDeployedVersion(t)
	gateKey1 := "ack-" + operatorVersion + "-e2e-api-change"
	gateKey2 := "ack-" + operatorVersion + "-e2e-storage-migration"

	tc.clearStaleAcks(t, gateKey1, gateKey2)

	t.Log("Creating labeled gate source ConfigMaps (simulating component/module chart output)")

	source1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-gate-source-1",
			Namespace: tc.OperatorNamespace,
			Labels:    map[string]string{gates.UpgradeGateLabel: "true"},
		},
		Data: map[string]string{
			gateKey1: "API change: review migration guide before proceeding",
		},
	}
	source2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-gate-source-2",
			Namespace: tc.OperatorNamespace,
			Labels:    map[string]string{gates.UpgradeGateLabel: "true"},
		},
		Data: map[string]string{
			gateKey2: "Storage migration: back up data before proceeding",
		},
	}

	tc.EventuallyResourceCreatedOrUpdated(WithObjectToCreate(source1))
	tc.EventuallyResourceCreatedOrUpdated(WithObjectToCreate(source2))

	defer tc.deleteGateSourceCMs(t, "e2e-gate-source-1", "e2e-gate-source-2")

	t.Log("Enabling dashboard and aigateway to trigger a reconcile")
	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.DataScienceCluster, tc.DataScienceClusterNamespacedName),
		WithMutateFunc(selectComponentsTransform("Managed", []string{"dashboard", "aigateway"})),
	)

	t.Log("Waiting for ProvisioningProgress=False with reason AdminAckRequired on Platform CR")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, tc.PlatformNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .status == "%s" and .reason == "%s")`,
			status.ConditionTypeProvisioningProgress, metav1.ConditionFalse, status.AdminAckRequiredReason,
		)),
		WithEventuallyTimeout(3*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
	t.Log("Confirmed: provisioning blocked by two unacknowledged gates")

	t.Logf("Acknowledging only the first gate: %s", gateKey1)
	tc.patchAcksConfigMap(t, gateKey1, "true")

	t.Log("Verifying provisioning remains blocked (second gate still unacknowledged)")
	tc.EnsureResourceExistsConsistently(
		WithMinimalObject(gvk.Platform, tc.PlatformNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .status == "%s" and .reason == "%s")`,
			status.ConditionTypeProvisioningProgress, metav1.ConditionFalse, status.AdminAckRequiredReason,
		)),
		WithConsistentlyDuration(20*time.Second),
		WithConsistentlyPollingInterval(5*time.Second),
	)
	t.Log("Confirmed: still blocked with one gate remaining")

	t.Logf("Acknowledging second gate: %s", gateKey2)
	tc.patchAcksConfigMap(t, gateKey2, "true")

	t.Log("Waiting for provisioning to resume (all gates acknowledged)")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, tc.PlatformNamespacedName),
		WithCondition(jq.Match(
			`any(.status.conditions[]; .type == "%s" and .reason != "%s")`,
			status.ConditionTypeProvisioningProgress, status.AdminAckRequiredReason,
		)),
		WithEventuallyTimeout(5*time.Minute),
		WithEventuallyPollingInterval(10*time.Second),
	)
	t.Log("Confirmed: provisioning resumed after all gates acknowledged")

	t.Log("Cleaning up: setting all components to Removed")
	tc.setAllRemoved(t)
}

func (tc *DAGOrderingTestCtx) clearStaleAcks(t *testing.T, keys ...string) {
	t.Helper()

	t.Logf("Clearing stale acks for keys %v (if present)", keys)
	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      gates.AcksConfigMap,
		}),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
			if data != nil {
				for _, k := range keys {
					delete(data, k)
				}
			}
			return unstructured.SetNestedStringMap(obj.Object, data, "data")
		}),
	)
}

func (tc *DAGOrderingTestCtx) patchAcksConfigMap(t *testing.T, key, value string) {
	t.Helper()

	tc.EventuallyResourcePatched(
		WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      gates.AcksConfigMap,
		}),
		WithMutateFunc(func(obj *unstructured.Unstructured) error {
			return unstructured.SetNestedField(obj.Object, value, "data", key)
		}),
	)
}

func (tc *DAGOrderingTestCtx) deleteGateSourceCMs(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		t.Logf("Removing gate source ConfigMap %s (if present)", name)
		tc.DeleteResource(
			WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
				Namespace: tc.OperatorNamespace,
				Name:      name,
			}),
			WithIgnoreNotFound(true),
			WithWaitForDeletion(true),
		)
	}
}
