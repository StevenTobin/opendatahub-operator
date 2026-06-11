package modules_test

import (
	"context"
	"errors"
	"testing"

	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/types"

	. "github.com/onsi/gomega"
)

type mockHandler struct {
	modules.BaseHandler

	enabled bool
}

func newMockHandler(name string, enabled bool) *mockHandler {
	return &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:   name,
				CRName: "default",
				GVK:    schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"},
			},
		},
		enabled: enabled,
	}
}

func (m *mockHandler) IsEnabled(_ *modules.PlatformContext) bool {
	return m.enabled
}

func (m *mockHandler) BuildModuleCR(_ context.Context, _ client.Client, _ *modules.PlatformContext) (*unstructured.Unstructured, error) {
	return nil, nil
}

// Verify mockHandler satisfies ModuleHandler at compile time.
var _ modules.ModuleHandler = (*mockHandler)(nil)

func TestRegistryAdd(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	h := newMockHandler("test-module", true)
	reg.Add(h)

	g.Expect(reg.IsEnabled("test-module")).Should(BeTrue())
	g.Expect(reg.HasEntries()).Should(BeTrue())
}

func TestRegistryDisableEnable(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	h := newMockHandler("test-module", true)
	reg.Add(h)

	g.Expect(reg.IsEnabled("test-module")).Should(BeTrue())

	reg.Disable("test-module")
	g.Expect(reg.IsEnabled("test-module")).Should(BeFalse())

	reg.Enable("test-module")
	g.Expect(reg.IsEnabled("test-module")).Should(BeTrue())
}

func TestRegistryDisableNonExistent(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	reg.Disable("does-not-exist")
	g.Expect(reg.IsEnabled("does-not-exist")).Should(BeFalse())
}

func TestRegistryForEachSkipsDisabled(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	reg.Add(newMockHandler("a", true))
	reg.Add(newMockHandler("b", true))
	reg.Disable("b")

	var visited []string
	err := reg.ForEach(func(h modules.ModuleHandler) error {
		visited = append(visited, h.GetName())
		return nil
	})

	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(visited).Should(ConsistOf("a"))
}

func TestRegistryForEachCollectsErrors(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	reg.Add(newMockHandler("a", true))
	reg.Add(newMockHandler("b", true))

	err := reg.ForEach(func(h modules.ModuleHandler) error {
		return errors.New("fail-" + h.GetName())
	})

	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("fail-a"))
	g.Expect(err.Error()).Should(ContainSubstring("fail-b"))
}

func TestRegistryEmptyForEachIsNoop(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	g.Expect(reg.HasEntries()).Should(BeFalse())

	err := reg.ForEach(func(_ modules.ModuleHandler) error {
		t.Fatal("should not be called")
		return nil
	})

	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestRegistryIsModuleEnabled(t *testing.T) {
	g := NewWithT(t)
	reg := &modules.Registry{}

	enabledHandler := newMockHandler("enabled-mod", true)
	disabledHandler := newMockHandler("disabled-mod", false)

	reg.Add(enabledHandler)
	reg.Add(disabledHandler)

	platform := &modules.PlatformContext{}

	g.Expect(reg.IsModuleEnabled("enabled-mod", platform)).Should(BeTrue())
	g.Expect(reg.IsModuleEnabled("disabled-mod", platform)).Should(BeFalse())
	g.Expect(reg.IsModuleEnabled("nonexistent", platform)).Should(BeFalse())

	reg.Disable("enabled-mod")
	g.Expect(reg.IsModuleEnabled("enabled-mod", platform)).Should(BeFalse())
}

func TestBaseHandlerDefaultsHelmOnly(t *testing.T) {
	g := NewWithT(t)

	h := &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:        "helm-mod",
				CRName:      "default",
				GVK:         schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"},
				ChartDir:    "mymodule",
				ReleaseName: "mymodule-operator",
			},
		},
		enabled: true,
	}

	g.Expect(h.GetName()).Should(Equal("helm-mod"))
	g.Expect(h.GetGVK()).Should(Equal(schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"}))

	platform := &modules.PlatformContext{ApplicationsNamespace: "test-ns"}
	manifests := h.GetOperatorManifests(platform)
	g.Expect(manifests.HelmCharts).Should(HaveLen(1))
	g.Expect(manifests.HelmCharts[0].ReleaseName).Should(Equal("mymodule-operator"))
	g.Expect(manifests.Manifests).Should(BeEmpty())
}

func TestBaseHandlerDefaultsKustomizeOnly(t *testing.T) {
	g := NewWithT(t)

	h := &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:        "kustomize-mod",
				CRName:      "default",
				GVK:         schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"},
				ManifestDir: "mymodule",
				ContextDir:  "operator",
				SourcePath:  "overlays/production",
			},
		},
		enabled: true,
	}

	manifests := h.GetOperatorManifests(nil)
	g.Expect(manifests.HelmCharts).Should(BeEmpty())
	g.Expect(manifests.Manifests).Should(HaveLen(1))
	g.Expect(manifests.Manifests[0].Path).Should(Equal("mymodule"))
	g.Expect(manifests.Manifests[0].ContextDir).Should(Equal("operator"))
	g.Expect(manifests.Manifests[0].SourcePath).Should(Equal("overlays/production"))
}

func TestBaseHandlerKustomizeManifestsBasePath(t *testing.T) {
	g := NewWithT(t)

	h := &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:        "kustomize-mod",
				CRName:      "default",
				GVK:         schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"},
				ManifestDir: "mymodule",
				ContextDir:  "operator",
				SourcePath:  "overlays/production",
			},
		},
		enabled: true,
	}

	platform := &modules.PlatformContext{ManifestsBasePath: "/opt/manifests"}
	manifests := h.GetOperatorManifests(platform)
	g.Expect(manifests.Manifests).Should(HaveLen(1))
	g.Expect(manifests.Manifests[0].Path).Should(Equal("/opt/manifests/mymodule"))
}

func TestBaseHandlerSourcePathFnOverridesSourcePath(t *testing.T) {
	g := NewWithT(t)

	h := &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{
				Name:        "kustomize-mod",
				CRName:      "default",
				GVK:         schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Mock"},
				ManifestDir: "mymodule",
				SourcePath:  "overlays/default",
				SourcePathFn: modules.PlatformOverlay(map[common.Platform]string{
					cluster.OpenDataHub:      "overlays/odh",
					cluster.SelfManagedRhoai: "overlays/rhoai",
				}),
			},
		},
		enabled: true,
	}

	platform := &modules.PlatformContext{
		Release: common.Release{Name: cluster.OpenDataHub},
	}
	manifests := h.GetOperatorManifests(platform)
	g.Expect(manifests.Manifests[0].SourcePath).Should(Equal("overlays/odh"))

	platformRhoai := &modules.PlatformContext{
		Release: common.Release{Name: cluster.SelfManagedRhoai},
	}
	manifests = h.GetOperatorManifests(platformRhoai)
	g.Expect(manifests.Manifests[0].SourcePath).Should(Equal("overlays/rhoai"))

	platformUnknown := &modules.PlatformContext{
		Release: common.Release{Name: "something-else"},
	}
	manifests = h.GetOperatorManifests(platformUnknown)
	g.Expect(manifests.Manifests[0].SourcePath).Should(Equal(""))
}

func TestConventionalOverlay(t *testing.T) {
	g := NewWithT(t)

	fn := modules.ConventionalOverlay()
	g.Expect(fn(common.Release{Name: cluster.OpenDataHub})).Should(Equal("overlays/" + string(cluster.OpenDataHub)))
	g.Expect(fn(common.Release{Name: cluster.SelfManagedRhoai})).Should(Equal("overlays/" + string(cluster.SelfManagedRhoai)))
	g.Expect(fn(common.Release{Name: ""})).Should(Equal(""))
}

func TestBaseHandlerDefaultsNoManifests(t *testing.T) {
	g := NewWithT(t)

	h := newMockHandler("empty", true)

	manifests := h.GetOperatorManifests(nil)
	g.Expect(manifests.HelmCharts).Should(BeEmpty())
	g.Expect(manifests.Manifests).Should(BeEmpty())
}

func TestParseConditions(t *testing.T) {
	g := NewWithT(t)

	u := &unstructured.Unstructured{
		Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             "True",
						"reason":             "AllGood",
						"message":            "Everything is fine",
						"observedGeneration": float64(3),
						"lastTransitionTime": "2026-04-22T10:30:00Z",
					},
					map[string]any{
						"type":   "Degraded",
						"status": "False",
					},
				},
			},
		},
	}

	conditions, err := modules.ParseConditions(u)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(conditions).Should(HaveLen(2))
	g.Expect(conditions[0].Type).Should(Equal("Ready"))
	g.Expect(conditions[0].Status).Should(Equal(metav1.ConditionTrue))
	g.Expect(conditions[0].Reason).Should(Equal("AllGood"))
	g.Expect(conditions[0].Message).Should(Equal("Everything is fine"))
	g.Expect(conditions[0].ObservedGeneration).Should(Equal(int64(3)))
	g.Expect(conditions[0].LastTransitionTime.IsZero()).Should(BeFalse())
	g.Expect(conditions[1].Type).Should(Equal("Degraded"))
	g.Expect(conditions[1].Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(conditions[1].ObservedGeneration).Should(Equal(int64(0)))
	g.Expect(conditions[1].LastTransitionTime.IsZero()).Should(BeTrue())
}

func TestExtractGVKFromCRD(t *testing.T) {
	g := NewWithT(t)

	crd := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"spec": map[string]any{
				"group": "services.platform.opendatahub.io",
				"names": map[string]any{
					"kind": "Monitoring",
				},
				"versions": []any{
					map[string]any{"name": "v1beta1", "served": true, "storage": false},
					map[string]any{"name": "v1alpha1", "served": true, "storage": true},
				},
			},
		},
	}

	gvk, err := modules.ExtractGVKFromCRD(crd)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(gvk.Group).Should(Equal("services.platform.opendatahub.io"))
	g.Expect(gvk.Version).Should(Equal("v1alpha1"))
	g.Expect(gvk.Kind).Should(Equal("Monitoring"))
}

func TestExtractGVKFromCRD_FallsBackToServed(t *testing.T) {
	g := NewWithT(t)

	crd := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"spec": map[string]any{
				"group": "test.io",
				"names": map[string]any{"kind": "Widget"},
				"versions": []any{
					map[string]any{"name": "v1", "served": true, "storage": false},
				},
			},
		},
	}

	gvk, err := modules.ExtractGVKFromCRD(crd)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(gvk.Version).Should(Equal("v1"))
}

func TestExtractGVKFromCRD_MissingGroup(t *testing.T) {
	g := NewWithT(t)

	crd := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"names":    map[string]any{"kind": "Widget"},
				"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}},
			},
		},
	}

	_, err := modules.ExtractGVKFromCRD(crd)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("spec.group"))
}

func TestExtractGVKFromCRD_NoVersions(t *testing.T) {
	g := NewWithT(t)

	crd := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"group":    "test.io",
				"names":    map[string]any{"kind": "Widget"},
				"versions": []any{},
			},
		},
	}

	_, err := modules.ExtractGVKFromCRD(crd)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("spec.versions"))
}

// --- ModuleImagesFromConfig tests ---

func TestModuleImagesFromConfig_ExplicitDeploymentName(t *testing.T) {
	g := NewWithT(t)

	cfg := &modules.ModuleConfig{
		Name:            "mymod",
		DeploymentName:  "custom-deploy",
		ContainerName:   "custom-container",
		ControllerImage: "RELATED_IMAGE_MY_OPERATOR",
		RelatedImages:   []string{"RELATED_IMAGE_A", "RELATED_IMAGE_B"},
	}

	imgs := modules.ModuleImagesFromConfig(cfg, modules.OperatorManifests{})
	g.Expect(imgs.DeploymentName).Should(Equal("custom-deploy"))
	g.Expect(imgs.ContainerName).Should(Equal("custom-container"))
	g.Expect(imgs.ControllerImage).Should(Equal("RELATED_IMAGE_MY_OPERATOR"))
	g.Expect(imgs.Images).Should(ConsistOf("RELATED_IMAGE_A", "RELATED_IMAGE_B"))
}

func TestModuleImagesFromConfig_FallsBackToHelmReleaseName(t *testing.T) {
	g := NewWithT(t)

	cfg := &modules.ModuleConfig{
		Name: "mymod",
	}
	manifests := modules.OperatorManifests{
		HelmCharts: []types.HelmChartInfo{
			{Source: helm.Source{ReleaseName: "helm-release"}},
		},
	}

	imgs := modules.ModuleImagesFromConfig(cfg, manifests)
	g.Expect(imgs.DeploymentName).Should(Equal("helm-release"))
}

func TestModuleImagesFromConfig_FallsBackToModuleName(t *testing.T) {
	g := NewWithT(t)

	cfg := &modules.ModuleConfig{Name: "mymod"}
	imgs := modules.ModuleImagesFromConfig(cfg, modules.OperatorManifests{})
	g.Expect(imgs.DeploymentName).Should(Equal("mymod"))
}

func TestModuleImagesFromConfig_DefaultContainerName(t *testing.T) {
	g := NewWithT(t)

	cfg := &modules.ModuleConfig{Name: "mymod"}
	imgs := modules.ModuleImagesFromConfig(cfg, modules.OperatorManifests{})
	g.Expect(imgs.ContainerName).Should(Equal("manager"))
}

// --- BaseHandler.IsEnabled default tests ---

func TestBaseHandlerIsEnabled_NilCallback(t *testing.T) {
	g := NewWithT(t)

	h := &mockHandler{
		BaseHandler: modules.BaseHandler{
			Config: modules.ModuleConfig{Name: "noconfig"},
		},
	}
	// mockHandler overrides IsEnabled, so test BaseHandler directly
	g.Expect(h.BaseHandler.IsEnabled(nil)).Should(BeFalse())
	g.Expect(h.BaseHandler.IsEnabled(&modules.PlatformContext{})).Should(BeFalse())
}

func TestBaseHandlerIsEnabled_WithCallback(t *testing.T) {
	g := NewWithT(t)

	h := modules.BaseHandler{
		Config: modules.ModuleConfig{
			Name: "withcb",
			IsEnabledFn: func(p *modules.PlatformContext) bool {
				return p != nil && p.ApplicationsNamespace == "test-ns"
			},
		},
	}

	g.Expect(h.IsEnabled(nil)).Should(BeFalse())
	g.Expect(h.IsEnabled(&modules.PlatformContext{ApplicationsNamespace: "other"})).Should(BeFalse())
	g.Expect(h.IsEnabled(&modules.PlatformContext{ApplicationsNamespace: "test-ns"})).Should(BeTrue())
}

// --- BaseHandler.BuildModuleCR default tests ---

func TestBaseHandlerBuildModuleCR_NoSpecAccessor(t *testing.T) {
	g := NewWithT(t)

	h := modules.BaseHandler{
		Config: modules.ModuleConfig{
			Name:   "nospa",
			CRName: "default",
			GVK:    schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Widget"},
		},
	}

	_, err := h.BuildModuleCR(context.Background(), nil, &modules.PlatformContext{})
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("not implemented"))
}

func TestBaseHandlerBuildModuleCR_NilPlatform(t *testing.T) {
	g := NewWithT(t)

	h := modules.BaseHandler{
		Config: modules.ModuleConfig{
			Name:   "nilplat",
			CRName: "default",
			GVK:    schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Widget"},
			SpecAccessor: func(_ *modules.PlatformContext) any {
				return map[string]any{"foo": "bar"}
			},
		},
	}

	_, err := h.BuildModuleCR(context.Background(), nil, nil)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("platform context is nil"))
}

func TestBaseHandlerBuildModuleCR_SpecAccessorReturnsNil(t *testing.T) {
	g := NewWithT(t)

	h := modules.BaseHandler{
		Config: modules.ModuleConfig{
			Name:   "nilspec",
			CRName: "default",
			GVK:    schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Widget"},
			SpecAccessor: func(_ *modules.PlatformContext) any {
				return nil
			},
		},
	}

	_, err := h.BuildModuleCR(context.Background(), nil, &modules.PlatformContext{})
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("SpecAccessor returned nil"))
}

type testSpec struct {
	ManagementState string `json:"managementState"`
	Namespace       string `json:"namespace,omitempty"`
}

func TestBaseHandlerBuildModuleCR_HappyPath(t *testing.T) {
	g := NewWithT(t)

	h := modules.BaseHandler{
		Config: modules.ModuleConfig{
			Name:   "happy",
			CRName: "default",
			GVK:    schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "Widget"},
			SpecAccessor: func(_ *modules.PlatformContext) any {
				return &testSpec{ManagementState: "Managed", Namespace: "test-ns"}
			},
		},
	}

	u, err := h.BuildModuleCR(context.Background(), nil, &modules.PlatformContext{})
	g.Expect(err).ShouldNot(HaveOccurred())

	g.Expect(u.GetKind()).Should(Equal("Widget"))
	g.Expect(u.GroupVersionKind().Group).Should(Equal("test.io"))
	g.Expect(u.GetName()).Should(Equal("default"))

	state, found, err := unstructured.NestedString(u.Object, "spec", "managementState")
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(found).Should(BeTrue())
	g.Expect(state).Should(Equal("Managed"))

	ns, found, err := unstructured.NestedString(u.Object, "spec", "namespace")
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(found).Should(BeTrue())
	g.Expect(ns).Should(Equal("test-ns"))
}

func TestParseConditionsNoStatus(t *testing.T) {
	g := NewWithT(t)

	u := &unstructured.Unstructured{
		Object: map[string]any{},
	}

	conditions, err := modules.ParseConditions(u)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(conditions).Should(BeNil())
}
