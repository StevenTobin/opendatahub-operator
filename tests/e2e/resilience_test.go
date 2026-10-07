package e2e_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/resources"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"

	. "github.com/onsi/gomega"
)

type OperatorResilienceTestCtx struct {
	*TestContext
}

// operatorResilienceTestSuite runs operator resilience and failure recovery tests.
func operatorResilienceTestSuite(t *testing.T) {
	t.Helper()

	tc, err := NewTestContext(t)
	require.NoError(t, err, "Failed to initialize test context")

	resilienceTestCtx := OperatorResilienceTestCtx{
		TestContext: tc,
	}

	testCases := []TestCase{
		{"Validate operator deployment health", resilienceTestCtx.ValidateOperatorDeployment},
		{"Validate leader election behavior", resilienceTestCtx.ValidateLeaderElectionBehavior},
		{"Validate RBAC restriction handling", resilienceTestCtx.ValidateRBACRestrictionHandling},
	}

	RunTestCases(t, testCases)
}

// ValidateOperatorDeployment checks if the operator deployment exists and is correctly configured.
func (tc *OperatorResilienceTestCtx) ValidateOperatorDeployment(t *testing.T) {
	t.Helper()

	skipUnless(t, Tier1)

	tc.validateDeploymentHealth(t)
}

// ValidateLeaderElectionBehavior validates that leader election works correctly when the current leader pod is deleted.
func (tc *OperatorResilienceTestCtx) ValidateLeaderElectionBehavior(t *testing.T) {
	t.Helper()

	skipUnless(t, Tier1)

	originalLeader := tc.findLeaderPodFromLeases()
	tc.g.Expect(originalLeader).ShouldNot(BeEmpty(), "Failed to find leader pod")

	tc.DeleteResource(
		WithMinimalObject(gvk.Pod, types.NamespacedName{
			Name:      originalLeader,
			Namespace: tc.OperatorNamespace,
		}),
		WithWaitForDeletion(true),
	)

	tc.g.Eventually(func() string {
		return tc.findLeaderPodFromLeases()
	}).Should(And(
		Not(BeEmpty()),
		Not(Equal(originalLeader)),
	), "New leader should be elected")

	tc.validateDeploymentHealth(t)
}

func (tc *OperatorResilienceTestCtx) ValidateRBACRestrictionHandling(t *testing.T) {
	t.Helper()

	skipUnless(t, Tier1)

	deploymentName := tc.getControllerDeploymentName()
	operatorDeployment := tc.FetchResource(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      deploymentName,
		}),
	)
	tc.g.Expect(operatorDeployment).NotTo(BeNil(), "Operator deployment not found")

	crbBackups, crbNames := tc.findAndBackupAllCRBsForServiceAccount(operatorDeployment)
	if len(crbBackups) == 0 {
		t.Fatalf("No ClusterRoleBinding found for the operator ServiceAccount")
	}

	restoreRBAC := func() {
		t.Log("Restoring all ClusterRoleBindings")
		for _, crbBackup := range crbBackups {
			tc.EventuallyResourceCreatedOrUpdated(WithObjectToCreate(crbBackup))
		}
	}
	t.Cleanup(restoreRBAC)

	tc.validateDeploymentHealth(t)

	t.Log("Deleting all ClusterRoleBindings")
	for _, crbName := range crbNames {
		tc.DeleteResource(WithMinimalObject(gvk.ClusterRoleBinding, types.NamespacedName{Name: crbName}))
	}

	podLabels, found, err := unstructured.NestedStringMap(
		operatorDeployment.Object,
		"spec",
		"selector",
		"matchLabels",
	)
	tc.g.Expect(err).NotTo(HaveOccurred(), "Failed to read the operator deployment pod selector")
	tc.g.Expect(found).To(BeTrue(), "Operator deployment pod selector not found")
	tc.g.Expect(podLabels).NotTo(BeEmpty(), "Operator deployment pod selector is empty")

	t.Log("Deleting operator Pods to force a restart")
	pods := tc.FetchResources(
		WithMinimalObject(gvk.Pod, types.NamespacedName{Namespace: tc.OperatorNamespace}),
		WithListOptions(&client.ListOptions{
			Namespace:     tc.OperatorNamespace,
			LabelSelector: labels.SelectorFromSet(podLabels),
		}),
	)
	for _, pod := range pods {
		tc.DeleteResource(
			WithMinimalObject(gvk.Pod, types.NamespacedName{
				Namespace: pod.GetNamespace(),
				Name:      pod.GetName(),
			}),
			WithWaitForDeletion(true),
		)
	}

	t.Log("Verifying operator becomes unhealthy")
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      deploymentName,
		}),
		WithCondition(jq.Match(`
			(.spec.replicas // 1) > 0 and
			(.status.readyReplicas // 0) < (.spec.replicas // 1)
		`)),
		WithEventuallyTimeout(tc.TestTimeouts.longEventuallyTimeout),
		WithCustomErrorMsg("Operator should fail without ClusterRoleBindings"),
	)

	restoreRBAC()

	tc.validateDeploymentHealth(t)
}

// findLeaderPodFromLeases finds current leader pod name from lease resources.
func (tc *OperatorResilienceTestCtx) findLeaderPodFromLeases() string {
	leases := tc.FetchResources(
		WithMinimalObject(gvk.Lease, types.NamespacedName{Namespace: tc.OperatorNamespace}),
		WithListOptions(&client.ListOptions{Namespace: tc.OperatorNamespace}),
	)

	for _, lease := range leases {
		if leaderPod := tc.extractLeaderFromLease(lease); leaderPod != "" {
			return leaderPod
		}
	}

	return ""
}

// extractLeaderFromLease extracts leader pod name from a lease object.
func (tc *OperatorResilienceTestCtx) extractLeaderFromLease(lease unstructured.Unstructured) string {
	holderIdentity, _, _ := unstructured.NestedString(lease.Object, "spec", "holderIdentity")

	controllerDeployment := tc.getControllerDeploymentName()
	if holderIdentity != "" && strings.Contains(holderIdentity, controllerDeployment) {
		// holderIdentity format is typically: "podname_uuid"
		// Extract the pod name (everything before the last underscore)
		lastUnderscoreIndex := strings.LastIndex(holderIdentity, "_")
		if lastUnderscoreIndex > 0 {
			podName := holderIdentity[:lastUnderscoreIndex]
			// Verify the pod name contains the deployment name
			if strings.Contains(podName, controllerDeployment) {
				return podName
			}
		}
	}

	return ""
}

// validateDeploymentHealth checks that the deployment's desired replicas are current and ready.
func (tc *OperatorResilienceTestCtx) validateDeploymentHealth(t *testing.T) {
	t.Helper()

	controllerDeployment := tc.getControllerDeploymentName()
	tc.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: tc.OperatorNamespace,
			Name:      controllerDeployment,
		}),
		WithCondition(And(
			jq.Match(`(.spec.replicas // 1) > 0`),
			jq.Match(`(.status.observedGeneration // 0) >= .metadata.generation`),
			jq.Match(`(.status.updatedReplicas // 0) == (.spec.replicas // 1)`),
			jq.Match(`(.status.readyReplicas // 0) == (.spec.replicas // 1)`),
			jq.Match(`(.status.availableReplicas // 0) == (.spec.replicas // 1)`),
		)),
		WithCustomErrorMsg("Operator deployment should have all desired replicas updated, ready, and available"),
	)
}

// findAndBackupAllCRBsForServiceAccount finds all ClusterRoleBindings referencing the given ServiceAccount and returns backup copies with their names.
func (tc *OperatorResilienceTestCtx) findAndBackupAllCRBsForServiceAccount(operatorDeployment *unstructured.Unstructured) ([]*unstructured.Unstructured, []string) {
	crbs := tc.FetchResources(
		WithMinimalObject(gvk.ClusterRoleBinding, types.NamespacedName{}),
	)

	saName, ok, err := unstructured.NestedString(operatorDeployment.Object, "spec", "template", "spec", "serviceAccountName")
	tc.g.Expect(err).NotTo(HaveOccurred(), "Failed to get serviceAccountName from operator deployment")
	tc.g.Expect(ok).To(BeTrue(), "serviceAccountName not found in operator deployment")

	var crbBackups []*unstructured.Unstructured
	var crbNames []string

	for _, obj := range crbs {
		subjects, found, _ := unstructured.NestedSlice(obj.Object, "subjects")
		if !found {
			continue
		}

		for _, subject := range subjects {
			subj, ok := subject.(map[string]any)
			if !ok {
				continue
			}

			kind, _ := subj["kind"].(string)
			if kind != gvk.ServiceAccount.Kind {
				continue
			}

			namespace, _ := subj["namespace"].(string)
			if namespace != tc.OperatorNamespace {
				continue
			}

			if name, _ := subj["name"].(string); name == saName {
				crbNames = append(crbNames, obj.GetName())
				crbBackups = append(crbBackups, resources.StripServerMetadata(&obj))
				break
			}
		}
	}

	return crbBackups, crbNames
}
