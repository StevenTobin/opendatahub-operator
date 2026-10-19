package e2e_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	dscApi "github.com/opendatahub-io/opendatahub-operator/v2/api/datasciencecluster/v3"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/status"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/dag"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/metadata/annotations"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/matchers/jq"

	. "github.com/onsi/gomega"
)

const moduleContractTimeout = dag.DefaultTimeout + 5*time.Minute

type moduleContractFinding struct {
	module  string
	message string
}

type moduleContractReport struct {
	mu       sync.Mutex
	findings []moduleContractFinding
}

func (r *moduleContractReport) record(module, message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.findings = append(r.findings, moduleContractFinding{module: module, message: message})
	r.mu.Unlock()

	// Keep module-owned contract findings distinct from platform/infrastructure
	// failures for test-retry and CI triage. The module name remains in the
	// selected child-test path and in the evidence emitted here.
	classification, err := json.Marshal(map[string]any{
		"category":    "test",
		"subcategory": "module-configuration",
		"error_code":  2001,
		"evidence":    []string{fmt.Sprintf("module=%s: %s", module, message)},
		"confidence":  "high",
	})
	if err == nil {
		fmt.Printf("FAILURE_CLASSIFICATION: %s\n", classification)
	}
}

func (r *moduleContractReport) hasFindingsFor(module string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, finding := range r.findings {
		if finding.module == module {
			return true
		}
	}
	return false
}

func (r *moduleContractReport) hasFindings() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.findings) > 0
}

func (r *moduleContractReport) log(t *testing.T) {
	t.Helper()
	if r == nil {
		return
	}
	r.mu.Lock()
	findings := append([]moduleContractFinding(nil), r.findings...)
	r.mu.Unlock()

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].module == findings[j].module {
			return findings[i].message < findings[j].message
		}
		return findings[i].module < findings[j].module
	})

	t.Log("============================================================")
	t.Log("MODULE CONTRACT REPORT")
	if len(findings) == 0 {
		t.Log("Status: PASS - all registered modules satisfy mandatory status conditions")
		t.Log("============================================================")
		return
	}

	t.Logf("Status: FINDINGS (non-fatal) - %d module contract finding(s)", len(findings))
	for _, finding := range findings {
		t.Logf("- Module: %s", finding.module)
		t.Logf("  Finding: %s", finding.message)
	}
	t.Log("============================================================")
	if testOpts.failOnModuleConformance && len(findings) > 0 {
		t.Errorf("module conformance reported %d module configuration finding(s)", len(findings))
	}
}

type ModuleContractTestCtx struct {
	*TestContext

	registration modulebuiltin.ModuleRegistration
	report       *moduleContractReport
}

func newModuleContractTestCtx(
	t *testing.T,
	parent *TestContext,
	registration modulebuiltin.ModuleRegistration,
	report *moduleContractReport,
) *ModuleContractTestCtx {
	t.Helper()

	// TestContext assertion helpers retain the *testing.T they were created
	// with. Rebind a shallow copy so a module failure is reported only on that
	// module's child test and cannot abort the DAG parent or its peer checks.
	child := *parent
	child.g = child.NewWithT(t)
	child.logger = t

	return &ModuleContractTestCtx{
		TestContext:  &child,
		registration: registration,
		report:       report,
	}
}

func (ctx *ModuleContractTestCtx) ValidateModuleContract(t *testing.T) {
	t.Helper()

	handler := ctx.registration.Handler
	moduleName := handler.GetName()
	expectedRelease := ctx.platformVersionFromConfigMap(t)
	moduleCR := ctx.discoverOwnedModuleCR(t)

	statusValid := ctx.validateModuleStatusEntries(t, moduleCR, expectedRelease)

	t.Run("operator Deployment", func(t *testing.T) {
		deploymentCtx := newModuleContractTestCtx(t, ctx.TestContext, ctx.registration, ctx.report)
		deployment := deploymentCtx.EnsureResourceExists(
			WithMinimalObject(gvk.Deployment, types.NamespacedName{
				Namespace: deploymentCtx.AppsNamespace,
				Name:      moduleDeploymentName(handler),
			}),
			WithCondition(And(
				jq.Match(`any(.status.conditions[]; .type == "Available" and .status == "True")`),
				jq.Match(`.status.observedGeneration == .metadata.generation`),
				jq.Match(`(.spec.replicas // 1) == (.status.updatedReplicas // 0)`),
				jq.Match(`(.spec.replicas // 1) == (.status.readyReplicas // 0)`),
				jq.Match(`(.status.unavailableReplicas // 0) == 0`),
			)),
			WithEventuallyTimeout(moduleContractTimeout),
			WithEventuallyPollingInterval(15*time.Second),
			WithCustomErrorMsg("module %s operator Deployment should be fully rolled out", moduleName),
		)
		deploymentCtx.validateInjectedEnv(t, deployment)
	})

	t.Run("source status propagation", func(t *testing.T) {
		if !statusValid {
			t.Logf("Skipping source readiness propagation for module %s because its status contract is invalid", moduleName)
			return
		}
		sourceCtx := newModuleContractTestCtx(t, ctx.TestContext, ctx.registration, ctx.report)
		sourceCtx.validateSourceStatusPropagation(t, moduleCR, expectedRelease)
	})

	if testOpts.failOnModuleConformance && ctx.report.hasFindingsFor(moduleName) {
		t.Errorf("module %s has module-configuration findings", moduleName)
	}
}

func (ctx *ModuleContractTestCtx) validateModuleStatusEntries(t *testing.T, moduleCR *unstructured.Unstructured, expectedRelease string) bool {
	t.Helper()

	moduleName := ctx.registration.Handler.GetName()
	valid := true
	if !moduleCRHasReadyCondition(moduleCR) {
		ctx.recordModuleMisconfiguration(t, moduleName, "Ready condition is not True")
		valid = false
	}
	if nestedObservedGeneration(moduleCR) != moduleCR.GetGeneration() {
		ctx.recordModuleMisconfiguration(t, moduleName, fmt.Sprintf("observedGeneration=%d does not match generation=%d", nestedObservedGeneration(moduleCR), moduleCR.GetGeneration()))
		valid = false
	}
	if !moduleCRHasPlatformRelease(moduleCR, expectedRelease) {
		currentReleases, _, _ := unstructured.NestedSlice(moduleCR.Object, "status", "releases")
		ctx.recordModuleMisconfiguration(t, moduleName, fmt.Sprintf("missing platform release %q in status.releases=%v", expectedRelease, currentReleases))
		valid = false
	}
	if !ctx.checkModuleCondition(t, moduleCR, status.ConditionTypeProvisioningSucceeded, metav1.ConditionTrue, true) {
		valid = false
	}
	if !ctx.checkModuleCondition(t, moduleCR, status.ConditionTypeDegraded, metav1.ConditionFalse, false) {
		valid = false
	}
	return valid
}

func (ctx *ModuleContractTestCtx) recordModuleMisconfiguration(t *testing.T, moduleName, message string) {
	t.Helper()
	ctx.report.record(moduleName, message)
	t.Logf("module %s configuration finding: %s", moduleName, message)
}

func nestedObservedGeneration(obj *unstructured.Unstructured) int64 {
	observedGeneration, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	return observedGeneration
}

func moduleCRHasReadyCondition(obj *unstructured.Unstructured) bool {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		conditionStatus, _ := condition["status"].(string)
		if conditionType == status.ConditionTypeReady && conditionStatus == string(metav1.ConditionTrue) {
			return true
		}
	}
	return false
}

func moduleCRHasPlatformRelease(obj *unstructured.Unstructured, expectedRelease string) bool {
	releases, found, _ := unstructured.NestedSlice(obj.Object, "status", "releases")
	if !found {
		return false
	}
	for _, item := range releases {
		release, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := release["name"].(string)
		version, _ := release["version"].(string)
		if name == common.PlatformReleaseName && version == expectedRelease {
			return true
		}
	}
	return false
}

func (ctx *ModuleContractTestCtx) checkModuleCondition(
	t *testing.T,
	moduleCR *unstructured.Unstructured,
	conditionType string,
	expectedStatus metav1.ConditionStatus,
	required bool,
) bool {
	t.Helper()

	moduleName := ctx.registration.Handler.GetName()
	conditions, found, err := unstructured.NestedSlice(moduleCR.Object, "status", "conditions")
	if err != nil {
		ctx.recordModuleMisconfiguration(t, moduleName, fmt.Sprintf("cannot read status.conditions: %v", err))
		return false
	}
	if !found {
		if required {
			ctx.recordModuleMisconfiguration(t, moduleName, "missing mandatory status.conditions")
			return false
		}
		return true
	}
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok || condition["type"] != conditionType {
			continue
		}
		actualStatus, _ := condition["status"].(string)
		if actualStatus != string(expectedStatus) {
			ctx.recordModuleMisconfiguration(t, moduleName, fmt.Sprintf(
				"mandatory condition %s has status %q, expected %q",
				conditionType, actualStatus, expectedStatus,
			))
			return false
		}
		return true
	}

	if required {
		ctx.recordModuleMisconfiguration(t, moduleName, fmt.Sprintf(
			"missing mandatory condition %s=%s", conditionType, expectedStatus,
		))
		return false
	}
	return true
}

func (ctx *ModuleContractTestCtx) discoverOwnedModuleCR(t *testing.T) *unstructured.Unstructured {
	t.Helper()

	ownerGVK, ownerNN := ctx.sourceOwner()
	owner := ctx.EnsureResourceExists(WithMinimalObject(ownerGVK, ownerNN))
	items := ctx.EnsureResourcesExist(
		WithMinimalObject(ctx.registration.Handler.GetGVK(), types.NamespacedName{}),
		WithCondition(ContainElement(jq.Match(
			`any(.metadata.ownerReferences[]; .apiVersion == "%s" and .kind == "%s" and .name == "%s" and .uid == "%s" and .controller == true and .blockOwnerDeletion == true)`,
			ownerGVK.GroupVersion().String(), ownerGVK.Kind, ownerNN.Name, owner.GetUID(),
		))),
		WithEventuallyTimeout(moduleContractTimeout),
		WithEventuallyPollingInterval(15*time.Second),
		WithCustomErrorMsg(
			"module %s CR should exist and be owned by %s %s",
			ctx.registration.Handler.GetName(), ownerGVK.Kind, ownerNN.Name,
		),
	)

	for i := range items {
		if !hasControllerOwnerReference(&items[i], ownerGVK, ownerNN.Name, owner.GetUID()) {
			continue
		}
		if namer, ok := ctx.registration.Handler.(interface{ GetCRName() string }); ok && namer.GetCRName() != "" {
			require.Equal(t, namer.GetCRName(), items[i].GetName(),
				"module %s CR should use its canonical singleton name", ctx.registration.Handler.GetName())
		}
		return &items[i]
	}

	require.FailNow(t, "owned module CR was not returned after successful discovery", "module: %s", ctx.registration.Handler.GetName())
	return nil
}

func (ctx *ModuleContractTestCtx) sourceOwner() (schema.GroupVersionKind, types.NamespacedName) {
	if ctx.registration.ConfigSource == modules.ConfigFromDSCI {
		return gvk.DSCInitialization, ctx.DSCInitializationNamespacedName
	}
	return gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName
}

func (ctx *ModuleContractTestCtx) validateInjectedEnv(t *testing.T, deployment *unstructured.Unstructured) {
	t.Helper()

	containerName := "manager"
	if namer, ok := ctx.registration.Handler.(modules.ContainerNamer); ok {
		containerName = namer.GetContainerName()
	}

	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	require.NoError(t, err, "read containers from module %s Deployment", ctx.registration.Handler.GetName())
	require.True(t, found, "module %s Deployment should declare containers", ctx.registration.Handler.GetName())

	var env map[string]string
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container["name"] != containerName {
			continue
		}
		env = containerEnv(container)
		break
	}

	require.NotNil(t, env, "module %s Deployment should contain container %q", ctx.registration.Handler.GetName(), containerName)
	require.Equal(t, ctx.AppsNamespace, env["APPLICATIONS_NAMESPACE"],
		"module %s Deployment should receive APPLICATIONS_NAMESPACE", ctx.registration.Handler.GetName())
	if ctx.MonitoringNamespace != "" {
		require.Equal(t, ctx.MonitoringNamespace, env["MONITORING_NAMESPACE"],
			"module %s Deployment should receive MONITORING_NAMESPACE", ctx.registration.Handler.GetName())
	}

	platform := ctx.EnsureResourceExists(WithMinimalObject(gvk.Platform, ctx.PlatformNamespacedName))
	require.Equal(t, platform.GetAnnotations()[annotations.PlatformType], env["ODH_MODULE_OPERATOR_PLATFORM_TYPE"],
		"module %s Deployment should receive ODH_MODULE_OPERATOR_PLATFORM_TYPE", ctx.registration.Handler.GetName())

	if provider, ok := ctx.registration.Handler.(modules.ExtraEnvProvider); ok {
		for name, expected := range provider.GetExtraEnv() {
			require.Equal(t, expected, env[name],
				"module %s Deployment should receive configured extra env %s", ctx.registration.Handler.GetName(), name)
		}
	}

	operatorEnv := ctx.platformOperatorEnv(t)
	for _, name := range ctx.registration.Handler.GetRelatedImages() {
		expected := operatorEnv[name]
		if expected == "" {
			continue
		}
		require.Equal(t, expected, env[name],
			"module %s Deployment should receive configured related image %s",
			ctx.registration.Handler.GetName(), name)
	}

	if imager, ok := ctx.registration.Handler.(modules.ControllerImager); ok && imager.GetControllerImage() != "" {
		expected := operatorEnv[imager.GetControllerImage()]
		if expected != "" {
			require.Equal(t, expected, containerImage(containers, containerName),
				"module %s Deployment should use configured controller image %s",
				ctx.registration.Handler.GetName(), imager.GetControllerImage())
			if namer, ok := ctx.registration.Handler.(modules.InitContainerNamer); ok && namer.GetInitContainerName() != "" {
				initContainers, _, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "initContainers")
				require.NoError(t, err, "read init containers from module %s Deployment", ctx.registration.Handler.GetName())
				require.Equal(t, expected, containerImage(initContainers, namer.GetInitContainerName()),
					"module %s Deployment init container %s should use configured controller image",
					ctx.registration.Handler.GetName(), namer.GetInitContainerName())
			}
		}
	}
}

func (ctx *ModuleContractTestCtx) platformOperatorEnv(t *testing.T) map[string]string {
	t.Helper()

	deployment := ctx.EnsureResourceExists(
		WithMinimalObject(gvk.Deployment, types.NamespacedName{
			Namespace: ctx.OperatorNamespace,
			Name:      ctx.getControllerDeploymentName(),
		}),
	)
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	require.NoError(t, err, "read platform operator containers")
	require.True(t, found, "platform operator Deployment should declare containers")

	for _, item := range containers {
		container, ok := item.(map[string]any)
		if ok && container["name"] == "manager" {
			return containerEnv(container)
		}
	}

	require.FailNow(t, "platform operator Deployment should contain manager container")
	return nil
}

func (ctx *ModuleContractTestCtx) validateSourceStatusPropagation(t *testing.T, moduleCR *unstructured.Unstructured, expectedRelease string) {
	t.Helper()

	if ctx.registration.ConfigSource == modules.ConfigFromDSCI {
		ctx.EnsureResourceExists(
			WithMinimalObject(gvk.DSCInitialization, ctx.DSCInitializationNamespacedName),
			WithCondition(And(
				jq.Match(`.spec.monitoring.managementState == "Managed"`),
				jq.Match(`.status.release.version == "%s"`, expectedRelease),
				jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, status.ConditionTypeReady, metav1.ConditionTrue),
				jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, status.ConditionTypeProvisioningSucceeded, metav1.ConditionTrue),
				jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, status.ConditionMonitoringReady, metav1.ConditionTrue),
			)),
			WithEventuallyTimeout(moduleContractTimeout),
			WithEventuallyPollingInterval(15*time.Second),
			WithCustomErrorMsg("DSCI should propagate module %s readiness", ctx.registration.Handler.GetName()),
		)
		return
	}

	readyType := ctx.registration.Handler.GetGVK().Kind + status.ReadySuffix
	if typer, ok := ctx.registration.Handler.(modules.ReadyConditionTyper); ok {
		readyType = typer.GetReadyConditionType()
	}
	componentStatusKey, hasComponentStatus := dscComponentStatusJSONKey(ctx.registration.Handler.GetName())
	if !hasComponentStatus {
		t.Logf("module %s has no typed DSC component status field; checking condition and source release only", ctx.registration.Handler.GetName())
		ctx.EnsureResourceExists(
			WithMinimalObject(gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName),
			WithCondition(And(
				jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, readyType, metav1.ConditionTrue),
				jq.Match(`.status.release.version == "%s"`, expectedRelease),
			)),
			WithEventuallyTimeout(moduleContractTimeout),
			WithEventuallyPollingInterval(15*time.Second),
			WithCustomErrorMsg("DSC should propagate module %s readiness and release", ctx.registration.Handler.GetName()),
		)
		return
	}

	dsc := ctx.EnsureResourceExists(
		WithMinimalObject(gvk.DataScienceCluster, ctx.DataScienceClusterNamespacedName),
		WithCondition(And(
			jq.Match(`any(.status.conditions[]; .type == "%s" and .status == "%s")`, readyType, metav1.ConditionTrue),
			jq.Match(`.status.release.version == "%s"`, expectedRelease),
			jq.Match(`.status.components.%s.managementState == "Managed"`, componentStatusKey),
			jq.Match(`any(.status.components.%s.releases[]; .name == "%s" and .version == "%s")`,
				componentStatusKey, common.PlatformReleaseName, expectedRelease),
		)),
		WithEventuallyTimeout(moduleContractTimeout),
		WithEventuallyPollingInterval(15*time.Second),
		WithCustomErrorMsg("DSC should propagate module %s readiness, release, and management state", ctx.registration.Handler.GetName()),
	)
	moduleReleases, _, err := unstructured.NestedSlice(moduleCR.Object, "status", "releases")
	require.NoError(t, err, "read module %s status releases", ctx.registration.Handler.GetName())
	dscReleases, _, err := unstructured.NestedSlice(dsc.Object, "status", "components", componentStatusKey, "releases")
	require.NoError(t, err, "read DSC status releases for module %s", ctx.registration.Handler.GetName())
	require.ElementsMatch(t, moduleReleases, dscReleases,
		"DSC should mirror all status releases from module %s", ctx.registration.Handler.GetName())
}

func (ctx *ModuleContractTestCtx) platformVersionFromConfigMap(t *testing.T) string {
	t.Helper()

	moduleName := ctx.registration.Handler.GetName()
	configMapName := modules.PlatformConfigName(moduleName)
	configMap := ctx.EnsureResourceExists(
		WithMinimalObject(gvk.ConfigMap, types.NamespacedName{
			Namespace: ctx.AppsNamespace,
			Name:      configMapName,
		}),
		WithCondition(jq.Match(`.data.%s != null and .data.%s != ""`,
			modules.PlatformVersionKey, modules.PlatformVersionKey)),
		WithEventuallyTimeout(moduleContractTimeout),
		WithEventuallyPollingInterval(15*time.Second),
		WithCustomErrorMsg("module %s platform ConfigMap should publish %s",
			moduleName, modules.PlatformVersionKey),
	)
	version, found, err := unstructured.NestedString(
		configMap.Object, "data", modules.PlatformVersionKey,
	)
	require.NoError(t, err, "read module %s platform ConfigMap version", moduleName)
	require.True(t, found, "module %s platform ConfigMap version should be present", moduleName)

	platform := ctx.EnsureResourceExists(
		WithMinimalObject(gvk.Platform, ctx.PlatformNamespacedName),
		WithCondition(jq.Match(`.metadata.annotations."%s" != null and .metadata.annotations."%s" != ""`,
			annotations.PlatformVersion, annotations.PlatformVersion)),
		WithEventuallyTimeout(moduleContractTimeout),
		WithEventuallyPollingInterval(15*time.Second),
	)
	operatorVersion := platform.GetAnnotations()[annotations.PlatformVersion]
	require.Equal(t, operatorVersion, version,
		"module %s ConfigMap %s must publish the operator platform version",
		moduleName, configMapName)
	return version
}

func hasControllerOwnerReference(obj *unstructured.Unstructured, ownerGVK schema.GroupVersionKind, name string, uid types.UID) bool {
	for _, owner := range obj.GetOwnerReferences() {
		if owner.APIVersion == ownerGVK.GroupVersion().String() && owner.Kind == ownerGVK.Kind && owner.Name == name &&
			owner.UID == uid && owner.Controller != nil && *owner.Controller &&
			owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion {
			return true
		}
	}
	return false
}

func dscComponentStatusJSONKey(moduleName string) (string, bool) {
	expectedKey := dscComponentField(moduleName)
	statusType := reflect.TypeFor[dscApi.ComponentsStatus]()
	for field := range statusType.Fields() {
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key == expectedKey {
			return key, true
		}
	}
	return "", false
}

func moduleDeploymentName(handler modules.ModuleHandler) string {
	if namer, ok := handler.(modules.DeploymentNamer); ok && namer.GetDeploymentName() != "" {
		return namer.GetDeploymentName()
	}

	manifests := handler.GetOperatorManifests(&modules.PlatformContext{})
	for _, chart := range manifests.HelmCharts {
		if chart.ReleaseName != "" {
			return chart.ReleaseName
		}
	}

	return handler.GetName()
}

func containerImage(containers []any, name string) string {
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container["name"] != name {
			continue
		}
		image, _ := container["image"].(string)
		return image
	}
	return ""
}

func containerEnv(container map[string]any) map[string]string {
	result := map[string]string{}
	env, _ := container["env"].([]any)
	for _, item := range env {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		if name != "" {
			result[name] = value
		}
	}
	return result
}
