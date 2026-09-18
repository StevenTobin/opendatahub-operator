package modules_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	helmRenderer "github.com/k8s-manifest-kit/renderer-helm/pkg"
	operatorv1 "github.com/openshift/api/operator/v1"
	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsinternalvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apiextensionsvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	configv1alpha1 "github.com/opendatahub-io/opendatahub-operator/v2/api/config/v1alpha1"
	dscv2 "github.com/opendatahub-io/opendatahub-operator/v2/api/datasciencecluster/v2"
	dsciv2 "github.com/opendatahub-io/opendatahub-operator/v2/api/dscinitialization/v2"
	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	aigatewayModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/aigateway"
	aipipelinesModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/aipipelines"
	dashboardModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/dashboard"
	feastModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/feastoperator"
	kserveModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/kserve"
	mcplifecycleoperatorModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/mcplifecycleoperator"
	mlflowOperatorModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/mlflowoperator"
	modelregistryModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/modelregistry"
	monitoringModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/monitoring"
	ogxModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/ogx"
	rayModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/ray"
	sparkoperatorModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/sparkoperator"
	trainerModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/trainer"
	workbenchesModule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/workbenches"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/manifests/kustomize"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/utils/test/fakeclient"

	. "github.com/onsi/gomega"
)

type artifactRoots struct {
	charts    string
	manifests string
}

type renderedResource struct {
	object unstructured.Unstructured
	origin string
}

type resourceID struct {
	group     string
	kind      string
	namespace string
	name      string
}

func (id resourceID) String() string {
	return fmt.Sprintf("%s/%s %s/%s", id.group, id.kind, id.namespace, id.name)
}

func resolveArtifactRoots(t *testing.T) artifactRoots {
	t.Helper()

	chartsRoot := os.Getenv("DEFAULT_CHARTS_PATH")
	if chartsRoot == "" {
		chartsRoot = filepath.Join("..", "..", "..", "opt", "charts")
	}
	manifestsRoot := os.Getenv("DEFAULT_MANIFESTS_PATH")
	if manifestsRoot == "" {
		manifestsRoot = filepath.Join("..", "..", "..", "opt", "manifests")
	}

	absChartsRoot, err := filepath.Abs(chartsRoot)
	if err != nil {
		t.Fatalf("failed to resolve charts root %s: %v", chartsRoot, err)
	}
	absManifestsRoot, err := filepath.Abs(manifestsRoot)
	if err != nil {
		t.Fatalf("failed to resolve manifests root %s: %v", manifestsRoot, err)
	}

	if _, chartsErr := os.Stat(absChartsRoot); chartsErr != nil {
		if _, manifestsErr := os.Stat(absManifestsRoot); manifestsErr != nil {
			t.Fatalf("module artifacts are required: charts root %s: %v; manifests root %s: %v; run make get-manifests",
				absChartsRoot, chartsErr, absManifestsRoot, manifestsErr)
		}
	}

	return artifactRoots{charts: absChartsRoot, manifests: absManifestsRoot}
}

var helmAllowedKinds = map[string]bool{
	"Deployment":                     true,
	"Service":                        true,
	"ServiceAccount":                 true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"Role":                           true,
	"RoleBinding":                    true,
	"ConfigMap":                      true,
	"CustomResourceDefinition":       true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"Issuer":                         true,
	"Certificate":                    true,
}

var kustomizeAllowedKinds = map[string]bool{
	"Deployment":                       true,
	"Service":                          true,
	"ServiceAccount":                   true,
	"ServiceMonitor":                   true,
	"ClusterRole":                      true,
	"ClusterRoleBinding":               true,
	"Role":                             true,
	"RoleBinding":                      true,
	"ConfigMap":                        true,
	"CustomResourceDefinition":         true,
	"SecurityContextConstraints":       true,
	"Namespace":                        true,
	"NetworkPolicy":                    true,
	"MutatingWebhookConfiguration":     true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
	"ValidatingWebhookConfiguration":   true,
	"Issuer":                           true,
	"Certificate":                      true,
}

// moduleHandlers returns every module handler registered by the platform operator.
func moduleHandlers() []modules.ModuleHandler {
	return []modules.ModuleHandler{
		aipipelinesModule.NewHandler(),
		dashboardModule.NewHandler(),
		monitoringModule.NewHandler(),
		aigatewayModule.NewHandler(),
		mcplifecycleoperatorModule.NewHandler(),
		mlflowOperatorModule.NewHandler(),
		modelregistryModule.NewHandler(),
		kserveModule.NewHandler(),
		ogxModule.NewHandler(),
		trainerModule.NewHandler(),
		workbenchesModule.NewHandler(),
		feastModule.NewHandler(),
		sparkoperatorModule.NewHandler(),
		rayModule.NewHandler(),
	}
}

func registeredModulePackagePaths(t *testing.T) []string {
	t.Helper()

	mainPath := filepath.Join("..", "..", "..", "cmd", "main.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), mainPath, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", mainPath, err)
	}

	importPaths := make(map[string]string, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("failed to unquote import %s: %v", imported.Path.Value, err)
		}

		alias := pathpkg.Base(importPath)
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		importPaths[alias] = importPath
	}

	var registered []string
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "existingModules" {
			return true
		}
		found = true

		if len(spec.Values) != 1 {
			t.Fatalf("existingModules must have exactly one initializer")
			return false
		}

		registry, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatalf("existingModules initializer must be a map literal")
			return false
		}

		registered = make([]string, 0, len(registry.Elts))
		for _, element := range registry.Elts {
			entry, ok := element.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("existingModules contains a non-keyed entry")
				return false
			}

			constructor, ok := entry.Value.(*ast.CallExpr)
			if !ok {
				t.Fatalf("existingModules entry must call a handler constructor")
				return false
			}

			selector, ok := constructor.Fun.(*ast.SelectorExpr)
			if !ok {
				t.Fatalf("existingModules constructor must be package-qualified")
				return false
			}

			packageIdent, ok := selector.X.(*ast.Ident)
			if !ok {
				t.Fatalf("existingModules constructor must use an imported package")
				return false
			}

			importPath, ok := importPaths[packageIdent.Name]
			if !ok {
				t.Fatalf("cannot resolve module handler import alias %q", packageIdent.Name)
				return false
			}
			registered = append(registered, importPath)
		}

		return false
	})

	if !found {
		t.Fatalf("existingModules registry not found in %s", mainPath)
	}

	return registered
}

func testedModulePackagePaths(t *testing.T) []string {
	t.Helper()

	handlers := moduleHandlers()
	packagePaths := make([]string, 0, len(handlers))
	for _, handler := range handlers {
		handlerType := reflect.TypeOf(handler)
		if handlerType == nil {
			t.Fatalf("moduleHandlers contains a nil handler")
		}
		for handlerType.Kind() == reflect.Pointer {
			handlerType = handlerType.Elem()
		}
		if handlerType.PkgPath() == "" {
			t.Fatalf("cannot determine package path for handler %q", handler.GetName())
		}
		packagePaths = append(packagePaths, handlerType.PkgPath())
	}

	return packagePaths
}

func TestModuleHandlersMatchRegistration(t *testing.T) {
	g := NewWithT(t)
	g.Expect(testedModulePackagePaths(t)).Should(ConsistOf(registeredModulePackagePaths(t)),
		"moduleHandlers must contain every handler registered in cmd/main.go existingModules")
}

func renderModuleArtifacts(
	t *testing.T,
	handler modules.ModuleHandler,
	platform *modules.PlatformContext,
) (modules.OperatorManifests, []renderedResource) {
	t.Helper()

	manifests := handler.GetOperatorManifests(platform)
	if len(manifests.HelmCharts)+len(manifests.Manifests) == 0 {
		t.Fatalf("module %s release %s declares no operator artifacts", handler.GetName(), platform.Release.Name)
	}

	var rendered []renderedResource
	for _, chartInfo := range manifests.HelmCharts {
		if _, err := os.Stat(chartInfo.Chart); err != nil {
			t.Fatalf("chart directory %s not accessible for module %s release %s (run make get-manifests): %v",
				chartInfo.Chart, handler.GetName(), platform.Release.Name, err)
		}

		renderer, err := helmRenderer.New([]helmRenderer.Source{{
			Chart:       chartInfo.Chart,
			ReleaseName: chartInfo.ReleaseName,
			Values:      chartInfo.Values,
		}})
		if err != nil {
			t.Fatalf("failed to create Helm renderer for module %s release %s: %v",
				handler.GetName(), platform.Release.Name, err)
		}

		resources, err := renderer.Process(t.Context(), nil)
		if err != nil {
			t.Fatalf("failed to render chart %s for module %s release %s: %v",
				chartInfo.Chart, handler.GetName(), platform.Release.Name, err)
		}
		if len(resources) == 0 {
			t.Fatalf("chart %s rendered zero resources for module %s release %s",
				chartInfo.Chart, handler.GetName(), platform.Release.Name)
		}
		for _, resource := range resources {
			rendered = append(rendered, renderedResource{object: resource, origin: "helm:" + chartInfo.Chart})
		}
	}

	for _, manifestInfo := range manifests.Manifests {
		renderPath := manifestInfo.String()
		if _, err := os.Stat(renderPath); err != nil {
			t.Fatalf("manifest path %s not accessible for module %s release %s (run make get-manifests): %v",
				renderPath, handler.GetName(), platform.Release.Name, err)
		}

		namespace := platform.ApplicationsNamespace
		if manifestInfo.Namespace != "" {
			namespace = manifestInfo.Namespace
		}
		var renderOpts []kustomize.RenderOptsFn
		if namespace != "" {
			renderOpts = append(renderOpts, kustomize.WithNamespace(namespace))
		}

		resources, err := kustomize.NewEngine().Render(renderPath, renderOpts...)
		if err != nil {
			t.Fatalf("failed to render manifests %s for module %s release %s: %v",
				renderPath, handler.GetName(), platform.Release.Name, err)
		}
		if len(resources) == 0 {
			t.Fatalf("manifest path %s rendered zero resources for module %s release %s",
				renderPath, handler.GetName(), platform.Release.Name)
		}
		for _, resource := range resources {
			rendered = append(rendered, renderedResource{object: resource, origin: "kustomize:" + renderPath})
		}
	}

	return manifests, rendered
}

func renderedResourceID(t *testing.T, resource renderedResource) resourceID {
	t.Helper()

	apiVersion := resource.object.GetAPIVersion()
	kind := resource.object.GetKind()
	name := resource.object.GetName()
	if apiVersion == "" || kind == "" || name == "" {
		t.Fatalf("rendered resource from %s must have apiVersion, kind, and metadata.name: %#v",
			resource.origin, resource.object.Object)
	}

	groupVersion, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		t.Fatalf("rendered resource %s from %s has invalid apiVersion %q: %v", name, resource.origin, apiVersion, err)
	}
	return resourceID{
		group:     groupVersion.Group,
		kind:      kind,
		namespace: resource.object.GetNamespace(),
		name:      name,
	}
}

func validateImageEnvName(t *testing.T, handler modules.ModuleHandler, name string) {
	t.Helper()
	if !strings.HasPrefix(name, "RELATED_IMAGE_") {
		t.Errorf("module %s image environment variable %q must start with RELATED_IMAGE_", handler.GetName(), name)
	}
	if errors := utilvalidation.IsEnvVarName(name); len(errors) > 0 {
		t.Errorf("module %s image environment variable %q is invalid: %v", handler.GetName(), name, errors)
	}
}

func expectedDeploymentName(handler modules.ModuleHandler, manifests modules.OperatorManifests) string {
	if namer, ok := handler.(modules.DeploymentNamer); ok && namer.GetDeploymentName() != "" {
		return namer.GetDeploymentName()
	}
	for _, chart := range manifests.HelmCharts {
		if chart.ReleaseName != "" {
			return chart.ReleaseName
		}
	}
	return handler.GetName()
}

func findNamedContainer(t *testing.T, deployment unstructured.Unstructured, fieldName, name string) map[string]any {
	t.Helper()

	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", fieldName)
	if err != nil {
		t.Fatalf("Deployment %s has invalid %s: %v", deployment.GetName(), fieldName, err)
	}
	if !found {
		t.Fatalf("Deployment %s has no %s", deployment.GetName(), fieldName)
	}

	var matches []map[string]any
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if ok && container["name"] == name {
			matches = append(matches, container)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("Deployment %s must contain exactly one %s named %q, found %d",
			deployment.GetName(), fieldName, name, len(matches))
	}
	return matches[0]
}

func validateDeploymentContract(
	t *testing.T,
	handler modules.ModuleHandler,
	manifests modules.OperatorManifests,
	deployments []unstructured.Unstructured,
) {
	t.Helper()

	if len(deployments) != 1 {
		t.Fatalf("module %s should render exactly one Deployment across all operator artifacts, found %d",
			handler.GetName(), len(deployments))
	}
	deployment := deployments[0]
	expectedName := expectedDeploymentName(handler, manifests)
	if deployment.GetName() != expectedName {
		t.Fatalf("module %s expected Deployment %q for image injection, rendered %q",
			handler.GetName(), expectedName, deployment.GetName())
	}

	containerName := "manager"
	if namer, ok := handler.(modules.ContainerNamer); ok {
		containerName = namer.GetContainerName()
	}
	container := findNamedContainer(t, deployment, "containers", containerName)

	if imager, ok := handler.(modules.ControllerImager); ok && imager.GetControllerImage() != "" {
		validateImageEnvName(t, handler, imager.GetControllerImage())
		if image, _ := container["image"].(string); image == "" {
			t.Errorf("module %s target container %q must have an image for controller image replacement",
				handler.GetName(), containerName)
		}
	}

	if namer, ok := handler.(modules.InitContainerNamer); ok && namer.GetInitContainerName() != "" {
		imager, hasControllerImage := handler.(modules.ControllerImager)
		if !hasControllerImage || imager.GetControllerImage() == "" {
			t.Errorf("module %s declares init container %q without a controller image source",
				handler.GetName(), namer.GetInitContainerName())
		}
		initContainer := findNamedContainer(t, deployment, "initContainers", namer.GetInitContainerName())
		if image, _ := initContainer["image"].(string); image == "" {
			t.Errorf("module %s init container %q must have an image for controller image replacement",
				handler.GetName(), namer.GetInitContainerName())
		}
	}

	seenImages := make(map[string]struct{}, len(handler.GetRelatedImages()))
	for _, image := range handler.GetRelatedImages() {
		validateImageEnvName(t, handler, image)
		if _, duplicate := seenImages[image]; duplicate {
			t.Errorf("module %s declares duplicate related image %q", handler.GetName(), image)
		}
		seenImages[image] = struct{}{}
	}
}

func validateRenderedModule(
	t *testing.T,
	handler modules.ModuleHandler,
	manifests modules.OperatorManifests,
	resources []renderedResource,
	seenResources map[resourceID]renderedResource,
	seenCRDKinds map[schema.GroupKind]renderedResource,
	moduleCRKinds map[schema.GroupKind]struct{},
) {
	t.Helper()

	var deployments []unstructured.Unstructured
	for _, resource := range resources {
		id := renderedResourceID(t, resource)
		allowedKinds := kustomizeAllowedKinds
		if strings.HasPrefix(resource.origin, "helm:") {
			allowedKinds = helmAllowedKinds
		}
		if !allowedKinds[id.kind] {
			t.Errorf("module %s contains disallowed resource kind %q (%s) from %s",
				handler.GetName(), id.kind, id, resource.origin)
		}

		if _, prohibited := moduleCRKinds[schema.GroupKind{Group: id.group, Kind: id.kind}]; prohibited {
			t.Errorf("module %s artifact %s rendered prohibited platform-managed module CR %s",
				handler.GetName(), resource.origin, id)
		}

		if previous, duplicate := seenResources[id]; duplicate {
			classification := "identical"
			if !reflect.DeepEqual(previous.object.Object, resource.object.Object) {
				classification = "conflicting"
			}
			t.Errorf("module %s rendered %s duplicate resource %s from %s; first rendered from %s",
				handler.GetName(), classification, id, resource.origin, previous.origin)
		} else {
			seenResources[id] = resource
		}

		if id.kind == "CustomResourceDefinition" {
			if resource.object.GetAPIVersion() != apiextensionsv1.SchemeGroupVersion.String() {
				t.Errorf("module %s rendered unsupported CRD apiVersion %q for %s from %s",
					handler.GetName(), resource.object.GetAPIVersion(), id, resource.origin)
				continue
			}
			crd := apiextensionsv1.CustomResourceDefinition{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resource.object.Object, &crd); err != nil {
				t.Errorf("failed to decode CRD %s from %s: %v", id.name, resource.origin, err)
			} else {
				groupKind := schema.GroupKind{Group: crd.Spec.Group, Kind: crd.Spec.Names.Kind}
				if previous, duplicate := seenCRDKinds[groupKind]; duplicate && previous.object.GetName() != resource.object.GetName() {
					t.Errorf("module %s rendered CRD %s for %s from %s, but CRD %s from %s already declares that GroupKind",
						handler.GetName(), resource.object.GetName(), groupKind, resource.origin,
						previous.object.GetName(), previous.origin)
				} else if !duplicate {
					seenCRDKinds[groupKind] = resource
				}
			}
		}

		if id.kind == "Deployment" {
			deployments = append(deployments, resource.object)
		}
	}

	validateDeploymentContract(t, handler, manifests, deployments)
}

func TestModuleManifestRendering(t *testing.T) {
	roots := resolveArtifactRoots(t)
	handlers := moduleHandlers()
	moduleCRKinds := make(map[schema.GroupKind]struct{}, len(handlers))
	for _, handler := range handlers {
		moduleCRKinds[handler.GetGVK().GroupKind()] = struct{}{}
	}

	for _, platform := range testPlatformContexts(roots.charts, roots.manifests) {
		t.Run(string(platform.Release.Name), func(t *testing.T) {
			seenResources := make(map[resourceID]renderedResource)
			seenCRDKinds := make(map[schema.GroupKind]renderedResource)
			for _, handler := range handlers {
				t.Run(handler.GetName(), func(t *testing.T) {
					manifests, resources := renderModuleArtifacts(t, handler, platform)
					validateRenderedModule(t, handler, manifests, resources, seenResources, seenCRDKinds, moduleCRKinds)
				})
			}
		})
	}
}

// testPlatformContexts returns PlatformContext values for each supported platform.
func testPlatformContexts(chartsBasePath, manifestsBasePath string) []*modules.PlatformContext {
	return []*modules.PlatformContext{
		{
			ApplicationsNamespace: "test-ns",
			ChartsBasePath:        chartsBasePath,
			ManifestsBasePath:     manifestsBasePath,
			Release:               common.Release{Name: cluster.OpenDataHub},
			Modules:               testEnabledPlatformModules(),
		},
		{
			ApplicationsNamespace: "test-ns",
			ChartsBasePath:        chartsBasePath,
			ManifestsBasePath:     manifestsBasePath,
			Release:               common.Release{Name: cluster.SelfManagedRhoai},
			Modules:               testEnabledPlatformModules(),
		},
	}
}

func testEnabledPlatformModules() *configv1alpha1.PlatformModules {
	managed := common.ManagementSpec{ManagementState: operatorv1.Managed}
	return &configv1alpha1.PlatformModules{
		AIPipelines:          managed,
		AIGateway:            managed,
		Dashboard:            managed,
		FeastOperator:        managed,
		Kserve:               managed,
		MCPLifecycleOperator: managed,
		MLflowOperator:       managed,
		ModelRegistry:        managed,
		Monitoring:           managed,
		OGX:                  managed,
		SparkOperator:        managed,
		Ray:                  managed,
		Trainer:              managed,
		Workbenches:          managed,
	}
}

func testDSCContext() *modules.DSCContext {
	return &modules.DSCContext{
		DSC:  &dscv2.DataScienceCluster{},
		DSCI: &dsciv2.DSCInitialization{},
	}
}

type renderedCRD struct {
	definition apiextensionsv1.CustomResourceDefinition
	origin     string
}

func renderedCRDs(t *testing.T, resources []renderedResource) []renderedCRD {
	t.Helper()

	var crds []renderedCRD
	for _, resource := range resources {
		if resource.object.GetKind() != "CustomResourceDefinition" ||
			resource.object.GetAPIVersion() != apiextensionsv1.SchemeGroupVersion.String() {
			continue
		}

		crd := apiextensionsv1.CustomResourceDefinition{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resource.object.Object, &crd); err != nil {
			t.Fatalf("failed to decode CRD %s from %s: %v", resource.object.GetName(), resource.origin, err)
		}
		crds = append(crds, renderedCRD{definition: crd, origin: resource.origin})
	}
	return crds
}

func targetCRDForHandler(t *testing.T, handler modules.ModuleHandler, crds []renderedCRD) renderedCRD {
	t.Helper()

	targetGVK := handler.GetGVK()
	var matches []renderedCRD
	for _, crd := range crds {
		if crd.definition.Spec.Group == targetGVK.Group && crd.definition.Spec.Names.Kind == targetGVK.Kind {
			matches = append(matches, crd)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("module %s must render exactly one target CRD for %s, found %d",
			handler.GetName(), targetGVK.GroupKind(), len(matches))
	}
	return matches[0]
}

func validateRenderedCRD(t *testing.T, rendered renderedCRD) {
	t.Helper()
	g := NewWithT(t)
	crd := &rendered.definition

	expectedName := crd.Spec.Names.Plural + "." + crd.Spec.Group
	g.Expect(crd.Name).Should(Equal(expectedName),
		"CRD from %s must be named <plural>.<group>", rendered.origin)

	internalCRD := &apiextensionsinternal.CustomResourceDefinition{}
	err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, internalCRD, nil)
	g.Expect(err).ShouldNot(HaveOccurred(), "failed to convert CRD %s from %s", crd.Name, rendered.origin)
	if err == nil {
		for _, version := range internalCRD.Spec.Versions {
			if version.Storage {
				internalCRD.Status.StoredVersions = append(internalCRD.Status.StoredVersions, version.Name)
			}
		}
		validationErrors := apiextensionsinternalvalidation.ValidateCustomResourceDefinition(t.Context(), internalCRD)
		g.Expect(validationErrors).Should(BeEmpty(), "CRD %s from %s is invalid: %s",
			crd.Name, rendered.origin, validationErrors.ToAggregate())
	}

	seenVersions := make(map[string]struct{}, len(crd.Spec.Versions))
	storageVersions := 0
	servedVersions := 0
	for _, version := range crd.Spec.Versions {
		if _, duplicate := seenVersions[version.Name]; duplicate {
			t.Errorf("CRD %s from %s contains duplicate version %q", crd.Name, rendered.origin, version.Name)
		}
		seenVersions[version.Name] = struct{}{}
		if version.Storage {
			storageVersions++
		}
		if version.Served {
			servedVersions++
		}
		if !version.Served && !version.Storage {
			continue
		}
		if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			t.Errorf("CRD %s version %s from %s must define an OpenAPI schema", crd.Name, version.Name, rendered.origin)
			continue
		}

		internalSchema := &apiextensionsinternal.JSONSchemaProps{}
		err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
			version.Schema.OpenAPIV3Schema, internalSchema, nil)
		g.Expect(err).ShouldNot(HaveOccurred(), "failed to convert schema for CRD %s version %s", crd.Name, version.Name)
		if err != nil {
			continue
		}
		structural, err := structuralschema.NewStructural(internalSchema)
		g.Expect(err).ShouldNot(HaveOccurred(), "failed to create structural schema for CRD %s version %s", crd.Name, version.Name)
		if err == nil {
			structuralErrors := structuralschema.ValidateStructural(field.NewPath("openAPIV3Schema"), structural)
			g.Expect(structuralErrors).Should(BeEmpty(), "CRD %s version %s has a non-structural schema: %s",
				crd.Name, version.Name, structuralErrors.ToAggregate())
		}
	}

	g.Expect(storageVersions).Should(Equal(1), "CRD %s from %s must have exactly one storage version", crd.Name, rendered.origin)
	g.Expect(servedVersions).Should(BeNumerically(">", 0), "CRD %s from %s must have at least one served version", crd.Name, rendered.origin)
}

func validateTargetCRDContract(t *testing.T, handler modules.ModuleHandler, rendered renderedCRD) *apiextensionsv1.JSONSchemaProps {
	t.Helper()
	g := NewWithT(t)
	crd := &rendered.definition
	targetGVK := handler.GetGVK()

	g.Expect(crd.Spec.Scope).Should(Equal(apiextensionsv1.ClusterScoped),
		"module %s target CRD %s must be cluster-scoped", handler.GetName(), crd.Name)

	for _, version := range crd.Spec.Versions {
		if version.Name != targetGVK.Version {
			continue
		}
		g.Expect(version.Served).Should(BeTrue(), "module %s target CRD version %s must be served",
			handler.GetName(), targetGVK.Version)
		g.Expect(version.Subresources).ShouldNot(BeNil(), "module %s target CRD version %s must define subresources",
			handler.GetName(), targetGVK.Version)
		if version.Subresources != nil {
			g.Expect(version.Subresources.Status).ShouldNot(BeNil(),
				"module %s target CRD version %s must enable the status subresource", handler.GetName(), targetGVK.Version)
		}
		g.Expect(version.Schema).ShouldNot(BeNil(), "module %s target CRD version %s must define a schema",
			handler.GetName(), targetGVK.Version)
		if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			return nil
		}

		statusSchema, found := version.Schema.OpenAPIV3Schema.Properties["status"]
		g.Expect(found).Should(BeTrue(), "module %s target CRD schema must define status", handler.GetName())
		if found {
			for _, fieldName := range []string{"conditions", "observedGeneration", "releases"} {
				g.Expect(statusSchema.Properties).Should(HaveKey(fieldName),
					"module %s target CRD status schema must define %s", handler.GetName(), fieldName)
			}
		}
		return version.Schema.OpenAPIV3Schema
	}

	t.Fatalf("module %s target CRD %s does not define version %s", handler.GetName(), crd.Name, targetGVK.Version)
	return nil
}

func TestModuleCRSchemaCompliance(t *testing.T) {
	roots := resolveArtifactRoots(t)
	cli, err := fakeclient.New()
	if err != nil {
		t.Fatalf("create fake client: %v", err)
	}
	dscCtx := testDSCContext()

	for _, platform := range testPlatformContexts(roots.charts, roots.manifests) {
		t.Run(string(platform.Release.Name), func(t *testing.T) {
			for _, handler := range moduleHandlers() {
				t.Run(handler.GetName(), func(t *testing.T) {
					_, resources := renderModuleArtifacts(t, handler, platform)
					crds := renderedCRDs(t, resources)
					if len(crds) == 0 {
						t.Fatalf("module %s release %s rendered no CRDs", handler.GetName(), platform.Release.Name)
					}
					for _, crd := range crds {
						validateRenderedCRD(t, crd)
					}

					targetCRD := targetCRDForHandler(t, handler, crds)
					v3Schema := validateTargetCRDContract(t, handler, targetCRD)
					if v3Schema == nil {
						t.FailNow()
					}

					internalSchema := &apiextensionsinternal.JSONSchemaProps{}
					if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v3Schema, internalSchema, nil); err != nil {
						t.Fatalf("failed to convert schema for module %s release %s: %v",
							handler.GetName(), platform.Release.Name, err)
					}
					validator, _, err := apiextensionsvalidation.NewSchemaValidator(internalSchema)
					if err != nil {
						t.Fatalf("failed to create schema validator for module %s release %s: %v",
							handler.GetName(), platform.Release.Name, err)
					}
					structural, err := structuralschema.NewStructural(internalSchema)
					if err != nil {
						t.Fatalf("failed to create structural schema for module %s release %s: %v",
							handler.GetName(), platform.Release.Name, err)
					}

					crConfig := &modules.ModuleCRConfig{
						ApplicationsNamespace: platform.ApplicationsNamespace,
						Release:               platform.Release,
					}
					cr, err := handler.BuildModuleCR(context.Background(), cli, dscCtx, crConfig)
					if err != nil {
						t.Fatalf("BuildModuleCR failed for module %s release %s: %v",
							handler.GetName(), platform.Release.Name, err)
					}
					if cr == nil {
						t.Fatalf("BuildModuleCR returned nil for module %s release %s",
							handler.GetName(), platform.Release.Name)
					}

					validationErrors := apiextensionsvalidation.ValidateCustomResource(field.NewPath(""), cr.Object, validator)
					if len(validationErrors) > 0 {
						t.Errorf("BuildModuleCR output for module %s release %s does not conform to CRD schema: %s",
							handler.GetName(), platform.Release.Name, validationErrors.ToAggregate())
					}

					candidate := cr.DeepCopy()
					prunedFields := pruning.PruneWithOptions(candidate.Object, structural, true, structuralschema.UnknownFieldPathOptions{
						TrackUnknownFieldPaths: true,
					})
					if len(prunedFields) > 0 {
						t.Errorf("BuildModuleCR output for module %s release %s contains fields absent from the CRD schema: %v",
							handler.GetName(), platform.Release.Name, prunedFields)
					}
				})
			}
		})
	}
}
