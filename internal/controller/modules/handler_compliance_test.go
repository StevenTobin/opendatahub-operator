package modules_test

import (
	"context"
	"testing"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	configv1alpha1 "github.com/opendatahub-io/opendatahub-operator/v2/api/config/v1alpha1"
	dscv2 "github.com/opendatahub-io/opendatahub-operator/v2/api/datasciencecluster/v2"
	dsciv2 "github.com/opendatahub-io/opendatahub-operator/v2/api/dscinitialization/v2"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/fakeclient"

	. "github.com/onsi/gomega"
)

func managedDSCContext() (*modules.DSCContext, *modules.ModuleCRConfig) {
	return &modules.DSCContext{
			DSC:  &dscv2.DataScienceCluster{},
			DSCI: &dsciv2.DSCInitialization{},
		}, &modules.ModuleCRConfig{
			ApplicationsNamespace: "opendatahub",
			Release:               common.Release{Name: "Open Data Hub"},
		}
}

func managedDSCPlatformContext() *modules.PlatformContext {
	return &modules.PlatformContext{
		ApplicationsNamespace: "opendatahub",
		Release:               common.Release{Name: "Open Data Hub"},
		ManifestsBasePath:     "/opt/manifests",
		ChartsBasePath:        "/opt/charts",
	}
}

func TestHandlerCompliance_GetNameIsNonEmpty(t *testing.T) {
	seen := make(map[string]bool)
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			name := h.GetName()
			g.Expect(name).ShouldNot(BeEmpty(), "GetName must return a non-empty string")
			g.Expect(seen).ShouldNot(HaveKey(name), "duplicate handler name %q", name)
			seen[name] = true
		})
	}
}

func TestHandlerCompliance_GetGVK(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			gvk := h.GetGVK()
			g.Expect(gvk.Group).ShouldNot(BeEmpty(), "GVK.Group must be set")
			g.Expect(gvk.Version).ShouldNot(BeEmpty(), "GVK.Version must be set")
			g.Expect(gvk.Kind).ShouldNot(BeEmpty(), "GVK.Kind must be set")
		})
	}
}

func TestHandlerCompliance_GetOperatorManifestsReturnsAtLeastOneSource(t *testing.T) {
	platform := managedDSCPlatformContext()
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			manifests := h.GetOperatorManifests(platform)
			g.Expect(len(manifests.HelmCharts)+len(manifests.Manifests)).Should(
				BeNumerically(">", 0),
				"handler must return at least one Helm chart or Kustomize manifest")
		})
	}
}

func TestHandlerCompliance_BuildModuleCR_ValidGVK(t *testing.T) {
	dscCtx, cfg := managedDSCContext()

	cli, err := fakeclient.New()
	if err != nil {
		t.Fatalf("create fake client: %v", err)
	}

	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			cr, err := h.BuildModuleCR(context.Background(), cli, dscCtx, cfg)
			g.Expect(err).ShouldNot(HaveOccurred())
			g.Expect(cr).ShouldNot(BeNil(), "registered handlers must build a module CR from a valid DSC/DSCI context")
			if cr == nil {
				return
			}
			g.Expect(cr.GetKind()).Should(Equal(h.GetGVK().Kind),
				"CR Kind must match handler GVK")
			g.Expect(cr.GroupVersionKind()).Should(Equal(h.GetGVK()),
				"CR GroupVersionKind must match handler GVK")
			g.Expect(cr.GetName()).ShouldNot(BeEmpty(),
				"CR must have a non-empty name")
			g.Expect(cr.GetNamespace()).Should(BeEmpty(),
				"module CRs must be cluster-scoped")
		})
	}
}

func TestHandlerCompliance_BuildModuleCR_NilContextReturnsError(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			cr, err := h.BuildModuleCR(context.Background(), nil, nil, nil)
			g.Expect(err).Should(HaveOccurred(),
				"BuildModuleCR should return an error when DSCContext is nil")
			g.Expect(cr).Should(BeNil(),
				"BuildModuleCR must not return a CR when DSCContext is nil")
		})
	}
}

func TestHandlerCompliance_IsEnabledFalseForNilPlatform(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(h.IsEnabled(nil)).Should(BeFalse(),
				"IsEnabled(nil) must return false")
		})
	}
}

func TestHandlerCompliance_IsEnabledFalseForEmptyPlatformModules(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)
			empty := &configv1alpha1.PlatformModules{}
			g.Expect(h.IsEnabled(empty)).Should(BeFalse(),
				"IsEnabled should return false when PlatformModules has no modules enabled")
		})
	}
}

func TestHandlerCompliance_WriteDSCComponentStatusDoesNotPanicOnNilDSC(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			NewWithT(t)
			h.WriteDSCComponentStatus(nil, true, nil)
			h.WriteDSCComponentStatus(nil, false, nil)
		})
	}
}

func TestHandlerCompliance_OptionalInterfaces(t *testing.T) {
	for _, h := range moduleHandlers() {
		t.Run(h.GetName(), func(t *testing.T) {
			g := NewWithT(t)

			if rct, ok := h.(modules.ReadyConditionTyper); ok {
				g.Expect(rct.GetReadyConditionType()).ShouldNot(BeEmpty(),
					"ReadyConditionTyper.GetReadyConditionType must return non-empty")
			}

			if cn, ok := h.(modules.ContainerNamer); ok {
				g.Expect(cn.GetContainerName()).ShouldNot(BeEmpty(),
					"ContainerNamer.GetContainerName must return non-empty")
			}

			if dn, ok := h.(modules.DeploymentNamer); ok {
				name := dn.GetDeploymentName()
				_ = name // may be empty — handler falls back to release name
			}

			if scp, ok := h.(modules.SubmoduleConditionProvider); ok {
				for _, sm := range scp.GetSubmoduleConditions() {
					g.Expect(sm.SourceConditionType).ShouldNot(BeEmpty(),
						"SubmoduleCondition.SourceConditionType must be set")
					g.Expect(sm.DSCConditionType).ShouldNot(BeEmpty(),
						"SubmoduleCondition.DSCConditionType must be set")
				}
			}
		})
	}
}
