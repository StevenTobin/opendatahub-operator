package modules

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	helm "github.com/k8s-manifest-kit/renderer-helm/pkg"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster/gvk"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/types"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/manifests/kustomize"
)

// ModuleConfig holds the static, declarative metadata for a module.
// Module teams populate this struct; BaseHandler provides default
// implementations of ModuleHandler methods that operate on it.
//
// Set either ChartDir (Helm) or ManifestDir (Kustomize) to select the
// manifest format. If both are set, both are returned.
type ModuleConfig struct {
	// Name is the unique identifier for this module (used as registry key).
	Name string

	// GVK is the GroupVersionKind of the module CR managed by this handler.
	GVK schema.GroupVersionKind

	// CRName is the singleton name of the module CR instance (e.g. "default").
	CRName string

	// Helm fields -- used when ChartDir is set.

	// ReleaseName is the Helm release name for the module operator chart.
	ReleaseName string

	// ChartDir is the chart directory name relative to DefaultChartsPath.
	ChartDir string

	// Values are additional Helm values passed when rendering the chart.
	Values map[string]any

	// NamespaceValueKey is the Helm value key used to set the operator
	// deployment namespace (e.g. "operatorNamespace", "namespace"). When
	// set, BaseHandler.GetOperatorManifests injects
	// platform.ApplicationsNamespace under this key. Leave empty if the
	// chart does not need a namespace override.
	NamespaceValueKey string

	// Kustomize fields -- used when ManifestDir is set.

	// ManifestDir is the directory name relative to rr.ManifestsBasePath
	// containing Kustomize overlays for the module operator.
	ManifestDir string

	// ContextDir is an optional subdirectory within ManifestDir.
	ContextDir string

	// SourcePath is an optional static overlay path within ContextDir.
	// For platform-specific overlays, use SourcePathFn instead.
	SourcePath string

	// SourcePathFn returns the overlay path for the current platform at
	// render time. Takes precedence over SourcePath when set. Use
	// PlatformOverlay or ConventionalOverlay helpers for common patterns.
	SourcePathFn func(common.Release) string

	// Namespace overrides the default ApplicationsNamespace for Kustomize
	// rendering. When empty, Kustomize uses ApplicationsNamespace. Set this
	// for modules that deploy into a dedicated namespace. For Helm modules,
	// use NamespaceValueKey or Values instead; this field is not wired into
	// Helm rendering.
	Namespace string

	// DeploymentName overrides the expected Deployment name for env injection.
	// When empty, the framework falls back to the Helm ReleaseName (for Helm
	// modules) or the module Name (for Kustomize modules). Set this only when
	// the actual Deployment name differs from these defaults.
	DeploymentName string

	// ContainerName is the name of the primary operator container in the
	// module's Deployment. Defaults to "manager" (the kubebuilder convention).
	// Override only if the module chart uses a different container name.
	ContainerName string

	// ControllerImage is the RELATED_IMAGE_* env var name whose value is the
	// fully-qualified image reference for this module's operator container.
	// When set and the env var is present on the platform operator process,
	// the inject action overwrites the target container's image field in the
	// rendered Deployment. Leave empty if the chart already bakes in the
	// correct image and no override is needed.
	ControllerImage string

	// RelatedImages lists RELATED_IMAGE_* environment variable names that the
	// module operator needs. The platform reads each name from its own process
	// environment (where the release pipeline sets digest-pinned references)
	// and injects them into the module operator's Deployment before apply.
	// Variables whose values are empty on the platform operator are skipped.
	RelatedImages []string

	// IsEnabledFn returns whether the module should be deployed for the given
	// platform context. When set, BaseHandler.IsEnabled uses this instead of
	// requiring an override. Receives the full PlatformContext so both
	// component modules (DSC) and service modules (DSCI) can be handled.
	IsEnabledFn func(platform *PlatformContext) bool

	// SpecAccessor extracts the module-specific spec from the platform
	// context. When set, BaseHandler.BuildModuleCR uses it to construct the
	// module CR automatically via runtime.DefaultUnstructuredConverter.
	// Handlers with complex CR construction (e.g. auth field projection)
	// can still override BuildModuleCR.
	SpecAccessor func(platform *PlatformContext) any
}

// BaseHandler provides default implementations for ModuleHandler methods
// that are purely mechanical. Module teams embed this struct and only
// override IsEnabled and BuildModuleCR.
type BaseHandler struct {
	Config ModuleConfig
}

func (b *BaseHandler) GetConfig() *ModuleConfig {
	return &b.Config
}

func (b *BaseHandler) GetName() string {
	return b.Config.Name
}

func (b *BaseHandler) GetGVK() schema.GroupVersionKind {
	return b.Config.GVK
}

// IsEnabled returns whether the module should be deployed. When
// Config.IsEnabledFn is set, it delegates to the callback. Otherwise
// it returns false — handlers without a callback must override this method.
func (b *BaseHandler) IsEnabled(platform *PlatformContext) bool {
	if b.Config.IsEnabledFn != nil {
		return b.Config.IsEnabledFn(platform)
	}
	return false
}

// BuildModuleCR constructs the module CR from the platform context. When
// Config.SpecAccessor is set, it converts the returned spec to unstructured
// and sets the GVK and CR name automatically. Handlers with complex CR
// construction should override this method.
func (b *BaseHandler) BuildModuleCR(
	_ context.Context,
	_ client.Client,
	platform *PlatformContext,
) (*unstructured.Unstructured, error) {
	if b.Config.SpecAccessor == nil {
		return nil, fmt.Errorf("module %s: BuildModuleCR not implemented and no SpecAccessor configured", b.Config.Name)
	}

	if platform == nil {
		return nil, fmt.Errorf("module %s: platform context is nil", b.Config.Name)
	}

	specObj := b.Config.SpecAccessor(platform)
	if specObj == nil {
		return nil, fmt.Errorf("module %s: SpecAccessor returned nil", b.Config.Name)
	}

	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(specObj)
	if err != nil {
		return nil, fmt.Errorf("module %s: converting spec to unstructured: %w", b.Config.Name, err)
	}

	u := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": spec,
		},
	}
	u.SetGroupVersionKind(b.Config.GVK)
	u.SetName(b.Config.CRName)

	return u, nil
}

// resolveGVK discovers the module's GVK from its CRD manifest when
// Config.GVK is not explicitly set. It renders the module's chart or
// overlay, scans for CustomResourceDefinition resources, and extracts
// group/version/kind from the CRD spec.
func (b *BaseHandler) resolveGVK(chartsBasePath, manifestsBasePath string) error {
	if b.Config.GVK != (schema.GroupVersionKind{}) {
		return nil
	}

	resources, err := b.renderManifests(chartsBasePath, manifestsBasePath)
	if err != nil {
		return fmt.Errorf("rendering manifests for GVK discovery in module %s: %w", b.Config.Name, err)
	}

	found := make([]schema.GroupVersionKind, 0, 1)
	for _, res := range resources {
		if res.GroupVersionKind() != gvk.CustomResourceDefinition {
			continue
		}
		extracted, err := ExtractGVKFromCRD(&res)
		if err != nil {
			return fmt.Errorf("extracting GVK from CRD in module %s: %w", b.Config.Name, err)
		}
		found = append(found, extracted)
	}

	switch len(found) {
	case 0:
		return fmt.Errorf("module %s: no CustomResourceDefinition found in rendered manifests", b.Config.Name)
	case 1:
		b.Config.GVK = found[0]
		return nil
	default:
		return fmt.Errorf("module %s: expected exactly 1 CRD, found %d", b.Config.Name, len(found))
	}
}

// renderManifests renders the module's Helm chart and/or Kustomize overlay
// without a PlatformContext, used for GVK discovery at startup.
func (b *BaseHandler) renderManifests(chartsBasePath, manifestsBasePath string) ([]unstructured.Unstructured, error) {
	var result []unstructured.Unstructured

	if b.Config.ChartDir != "" {
		chartPath := filepath.Join(chartsBasePath, b.Config.ChartDir)
		renderer, err := helm.New([]helm.Source{{Chart: chartPath, ReleaseName: b.Config.ReleaseName}})
		if err != nil {
			return nil, fmt.Errorf("creating helm renderer: %w", err)
		}
		resources, err := renderer.Process(context.Background(), nil)
		if err != nil {
			return nil, fmt.Errorf("rendering chart %s: %w", chartPath, err)
		}
		result = append(result, resources...)
	}

	if b.Config.ManifestDir != "" {
		manifestPath := b.Config.ManifestDir
		if manifestsBasePath != "" {
			manifestPath = filepath.Join(manifestsBasePath, manifestPath)
		}
		if b.Config.ContextDir != "" {
			manifestPath = filepath.Join(manifestPath, b.Config.ContextDir)
		}
		if b.Config.SourcePath != "" {
			manifestPath = filepath.Join(manifestPath, b.Config.SourcePath)
		}

		ke := kustomize.NewEngine()
		resources, err := ke.Render(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("rendering kustomize %s: %w", manifestPath, err)
		}
		result = append(result, resources...)
	}

	return result, nil
}

// ExtractGVKFromCRD extracts the GroupVersionKind from a CustomResourceDefinition's
// spec. It reads spec.group, spec.names.kind, and selects the storage version
// (falling back to the first served version).
func ExtractGVKFromCRD(crd *unstructured.Unstructured) (schema.GroupVersionKind, error) {
	group, _, err := unstructured.NestedString(crd.Object, "spec", "group")
	if err != nil || group == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("CRD %s missing spec.group", crd.GetName())
	}

	kind, _, err := unstructured.NestedString(crd.Object, "spec", "names", "kind")
	if err != nil || kind == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("CRD %s missing spec.names.kind", crd.GetName())
	}

	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("CRD %s missing spec.versions", crd.GetName())
	}

	version := ""
	for _, v := range versions {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _ := vm["name"].(string)
		if name == "" {
			continue
		}
		storage, _ := vm["storage"].(bool)
		if storage {
			version = name
			break
		}
		served, _ := vm["served"].(bool)
		if served && version == "" {
			version = name
		}
	}

	if version == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("CRD %s has no storage or served version", crd.GetName())
	}

	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}, nil
}

// InitModuleGVKs resolves GVKs for all registered modules that don't have
// an explicit GVK set. Must be called before Build and registerModuleCROwnedTypes.
func InitModuleGVKs(chartsBasePath, manifestsBasePath string) error {
	reg := DefaultRegistry()
	if !reg.HasEntries() {
		return nil
	}

	return reg.ForAll(func(h ModuleHandler, _ bool) error {
		type gvkResolver interface {
			resolveGVK(chartsBasePath, manifestsBasePath string) error
		}
		if r, ok := h.(gvkResolver); ok {
			return r.resolveGVK(chartsBasePath, manifestsBasePath)
		}
		return nil
	})
}

func (b *BaseHandler) GetOperatorManifests(platform *PlatformContext) OperatorManifests {
	var result OperatorManifests

	if b.Config.ChartDir != "" && platform != nil {
		vals := make(map[string]any, len(b.Config.Values))
		for k, v := range b.Config.Values {
			vals[k] = v
		}

		if b.Config.NamespaceValueKey != "" && platform.ApplicationsNamespace != "" {
			vals[b.Config.NamespaceValueKey] = platform.ApplicationsNamespace
		}

		result.HelmCharts = []types.HelmChartInfo{{
			Source: helm.Source{
				Chart:       filepath.Join(platform.ChartsBasePath, b.Config.ChartDir),
				ReleaseName: b.Config.ReleaseName,
				Values:      helm.Values(vals),
			},
		}}
	}

	if b.Config.ManifestDir != "" {
		manifestPath := b.Config.ManifestDir
		if platform != nil && platform.ManifestsBasePath != "" {
			manifestPath = filepath.Join(platform.ManifestsBasePath, manifestPath)
		}

		sourcePath := b.Config.SourcePath
		if b.Config.SourcePathFn != nil && platform != nil {
			sourcePath = b.Config.SourcePathFn(platform.Release)
		}

		result.Manifests = []types.ManifestInfo{{
			Path:       manifestPath,
			ContextDir: b.Config.ContextDir,
			SourcePath: sourcePath,
			Namespace:  b.Config.Namespace,
		}}
	}

	return result
}

// GetModuleStatus reads the module CR by GVK+CRName and extracts status
// conditions and generation metadata for staleness detection.
//
// This default implementation performs a cluster-scoped Get (no namespace),
// which is correct for the required cluster-scoped module CRDs. Modules
// with namespace-scoped CRs would need to override this method.
func (b *BaseHandler) GetModuleStatus(ctx context.Context, cli client.Client) (*ModuleStatus, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(b.Config.GVK)
	u.SetName(b.Config.CRName)

	if err := cli.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
		return nil, err
	}

	conditions, err := ParseConditions(u)
	if err != nil {
		return nil, err
	}

	observedGen, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")

	return &ModuleStatus{
		Conditions:         conditions,
		ObservedGeneration: observedGen,
		Generation:         u.GetGeneration(),
	}, nil
}

// GetModuleCRState returns the lifecycle state of the module CR. It
// distinguishes between absent, alive, and being-deleted (has
// deletionTimestamp but finalizers are still being processed).
func (b *BaseHandler) GetModuleCRState(ctx context.Context, cli client.Client) (CRState, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(b.Config.GVK)

	err := cli.Get(ctx, client.ObjectKey{Name: b.Config.CRName}, u)
	if err != nil {
		if k8serr.IsNotFound(err) || meta.IsNoMatchError(err) {
			return CRStateAbsent, nil
		}
		return CRStateAbsent, err
	}

	if !u.GetDeletionTimestamp().IsZero() {
		return CRStateDeleting, nil
	}

	return CRStateAlive, nil
}

// DeleteModuleCR deletes the module CR from the cluster. Returns nil if the
// CR or its CRD does not exist, making the call idempotent.
func (b *BaseHandler) DeleteModuleCR(ctx context.Context, cli client.Client) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(b.Config.GVK)
	u.SetName(b.Config.CRName)

	if err := cli.Delete(ctx, u); err != nil {
		if k8serr.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("deleting module CR %s/%s: %w", b.Config.GVK.Kind, b.Config.CRName, err)
	}

	logf.FromContext(ctx).Info("deleted module CR",
		"module", b.Config.Name,
		"kind", b.Config.GVK.Kind,
		"name", b.Config.CRName)

	return nil
}

// DeleteOperatorResources renders the module's operator manifests (Helm and/or
// Kustomize) and deletes each resource from the cluster. NotFound errors are
// silently ignored so the operation is idempotent.
func (b *BaseHandler) DeleteOperatorResources(ctx context.Context, cli client.Client, platform *PlatformContext) error {
	log := logf.FromContext(ctx)
	manifests := b.GetOperatorManifests(platform)

	for _, chartInfo := range manifests.HelmCharts {
		renderer, err := helm.New([]helm.Source{chartInfo.Source})
		if err != nil {
			return fmt.Errorf("creating helm renderer for %s: %w", b.Config.Name, err)
		}

		resources, err := renderer.Process(ctx, nil)
		if err != nil {
			return fmt.Errorf("rendering chart for %s: %w", b.Config.Name, err)
		}

		if err := b.deleteRenderedResources(ctx, cli, log, resources); err != nil {
			return err
		}
	}

	for _, manifestInfo := range manifests.Manifests {
		ke := kustomize.NewEngine()
		ns := ""
		if platform != nil {
			ns = platform.ApplicationsNamespace
		}
		if manifestInfo.Namespace != "" {
			ns = manifestInfo.Namespace
		}

		var renderOpts []kustomize.RenderOptsFn
		if ns != "" {
			renderOpts = append(renderOpts, kustomize.WithNamespace(ns))
		}

		resources, err := ke.Render(manifestInfo.String(), renderOpts...)
		if err != nil {
			return fmt.Errorf("rendering kustomize manifests for %s: %w", b.Config.Name, err)
		}

		if err := b.deleteRenderedResources(ctx, cli, log, resources); err != nil {
			return err
		}
	}

	return nil
}

func (b *BaseHandler) deleteRenderedResources(
	ctx context.Context,
	cli client.Client,
	log logr.Logger,
	resources []unstructured.Unstructured,
) error {
	for i := range resources {
		res := &resources[i]

		if res.GroupVersionKind() == gvk.CustomResourceDefinition {
			log.V(1).Info("skipping CRD deletion during module cleanup",
				"module", b.Config.Name,
				"name", res.GetName())
			continue
		}

		log.V(1).Info("deleting module operator resource",
			"module", b.Config.Name,
			"kind", res.GetKind(),
			"name", res.GetName(),
			"namespace", res.GetNamespace())

		if err := cli.Delete(ctx, res); err != nil {
			if !k8serr.IsNotFound(err) && !meta.IsNoMatchError(err) {
				return fmt.Errorf("deleting %s %s/%s for module %s: %w",
					res.GetKind(), res.GetNamespace(), res.GetName(), b.Config.Name, err)
			}
		}
	}

	return nil
}

// PlatformOverlay returns a SourcePathFn that maps platform names to overlay
// paths. Use for modules with different Kustomize overlays per platform.
func PlatformOverlay(m map[common.Platform]string) func(common.Release) string {
	return func(r common.Release) string {
		if sp, ok := m[r.Name]; ok {
			return sp
		}
		return ""
	}
}

// ConventionalOverlay returns a SourcePathFn that resolves the overlay path
// as "overlays/<platformName>". Use when the module's manifest directory
// follows the convention of naming overlay subdirectories after platforms.
func ConventionalOverlay() func(common.Release) string {
	return func(r common.Release) string {
		if r.Name == "" {
			return ""
		}
		return filepath.Join("overlays", string(r.Name))
	}
}

// ParseConditions extracts []metav1.Condition from an unstructured object's
// .status.conditions field.
func ParseConditions(u *unstructured.Unstructured) ([]metav1.Condition, error) {
	rawConditions, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, nil
	}

	conditions := make([]metav1.Condition, 0, len(rawConditions))

	for _, raw := range rawConditions {
		cm, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		c := metav1.Condition{}

		if v, ok := cm["type"].(string); ok {
			c.Type = v
		}

		if v, ok := cm["status"].(string); ok {
			c.Status = metav1.ConditionStatus(v)
		}

		if v, ok := cm["reason"].(string); ok {
			c.Reason = v
		}

		if v, ok := cm["message"].(string); ok {
			c.Message = v
		}

		if v, ok := cm["observedGeneration"].(int64); ok {
			c.ObservedGeneration = v
		} else if v, ok := cm["observedGeneration"].(float64); ok {
			c.ObservedGeneration = int64(v)
		}

		if v, ok := cm["lastTransitionTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				c.LastTransitionTime = metav1.NewTime(t)
			}
		}

		if c.Type == "" || c.Status == "" {
			continue
		}

		conditions = append(conditions, c)
	}

	return conditions, nil
}
