package validate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const testOverlaysODH = "overlays/odh"

func TestExtractComponentKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "API constant",
			content: `ComponentName = componentApi.DashboardComponentName`,
			want:    "dashboard",
		},
		{
			name:    "lowercase componentName",
			content: `componentName = componentApi.KserveComponentName`,
			want:    "kserve",
		},
		{
			name:    "direct string",
			content: `ComponentName = "dashboard"`,
			want:    "dashboard",
		},
		{
			name:    "unknown constant",
			content: `ComponentName = componentApi.UnknownComponentName`,
			want:    "",
		},
		{
			name:    "no match",
			content: `package foo`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractComponentKey(tt.content)
			if got != tt.want {
				t.Errorf("extractComponentKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSourcePaths_PlatformMap(t *testing.T) {
	content := `
var ManifestsSourcePath = map[common.Platform]string{
	cluster.SelfManagedRhoai: "overlays/rhoai",
	cluster.ManagedRhoai:     "overlays/rhoai",
	cluster.OpenDataHub:      "overlays/odh",
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != testOverlaysODH {
		t.Errorf("expected [%s], got %v", testOverlaysODH, paths)
	}
}

func TestExtractSourcePaths_MultipleMaps(t *testing.T) {
	content := `
var overlaysSourcePaths = map[common.Platform]string{
	cluster.SelfManagedRhoai: "/rhoai/onprem",
	cluster.ManagedRhoai:     "/rhoai/addon",
	cluster.OpenDataHub:      "/odh",
}

var observabilitySourcePaths = map[common.Platform]string{
	cluster.SelfManagedRhoai: "observability/rhoai",
	cluster.ManagedRhoai:     "observability/rhoai",
	cluster.OpenDataHub:      "observability/odh",
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	sort.Strings(paths)
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
	if paths[0] != "observability/odh" || paths[1] != "odh" {
		t.Errorf("expected [observability/odh, odh], got %v", paths)
	}
}

func TestExtractSourcePaths_DirectLiteral(t *testing.T) {
	content := `
func manifestPath() types.ManifestInfo {
	return types.ManifestInfo{
		Path:       odhdeploy.DefaultManifestPath,
		ContextDir: ComponentName,
		SourcePath: "openshift",
	}
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != "openshift" {
		t.Errorf("expected [openshift], got %v", paths)
	}
}

func TestExtractSourcePaths_MixedPatternsDeduplication(t *testing.T) {
	content := `
var ManifestsSourcePath = map[common.Platform]string{
	cluster.OpenDataHub: "overlays/odh",
}

func manifestPath(p common.Platform) types.ManifestInfo {
	return types.ManifestInfo{
		Path:       odhdeploy.DefaultManifestPath,
		ContextDir: ComponentName,
		SourcePath: ManifestsSourcePath[p],
	}
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != testOverlaysODH {
		t.Errorf("expected [%s], got %v", testOverlaysODH, paths)
	}
}

func TestExtractSourcePaths_LeadingSlashStripped(t *testing.T) {
	content := `
var overlaysSourcePaths = map[common.Platform]string{
	cluster.OpenDataHub: "/odh",
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != "odh" {
		t.Errorf("expected [odh], got %v", paths)
	}
}

func TestExtractSourcePaths_ConstString(t *testing.T) {
	content := `
const BaseManifestsSourcePath = "overlays/odh"

func baseManifestInfo(sourcePath string) odhtypes.ManifestInfo {
	return odhtypes.ManifestInfo{
		Path:       deploy.DefaultManifestPath,
		ContextDir: ComponentName,
		SourcePath: sourcePath,
	}
}
`
	// Strategy 2 (sourcePathLiteral) can't resolve SourcePath: sourcePath since
	// it's a variable, but Strategy 3 (pathConstPattern) catches the const
	// declaration directly.
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != testOverlaysODH {
		t.Errorf("expected [%s] from const declaration, got %v", testOverlaysODH, paths)
	}
}

func TestParseComponentSourcePaths_RealCodebase(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	componentsDir := filepath.Join(repoRoot, "internal", "controller", "components")
	if _, err := os.Stat(componentsDir); os.IsNotExist(err) {
		t.Skip("real codebase not found at expected path, skipping")
	}

	result, err := ParseComponentSourcePaths(repoRoot, PlatformODH)
	if err != nil {
		t.Fatalf("ParseComponentSourcePaths failed: %v", err)
	}

	// Should find paths for most components. Note "maas" not "modelsasservice"
	// because scriptKeyOverrides remaps it.
	expectedComponents := []string{
		"dashboard",
		"kserve",
		"ray",
		"trustyai",
		"sparkoperator",
		"datasciencepipelines",
		"feastoperator",
		"llamastackoperator",
		"mlflowoperator",
		"modelcontroller",
		"maas",
	}

	for _, comp := range expectedComponents {
		cp, ok := result[comp]
		if !ok {
			t.Errorf("missing component %q in parsed results", comp)
			continue
		}
		if len(cp.SourcePaths) == 0 {
			t.Errorf("component %q has no source paths", comp)
		}
	}

	// Verify specific expected paths for well-known components.
	verifyPaths := map[string][]string{
		"kserve":          {"overlays/odh", "overlays/odh-xks"},
		"ray":             {"openshift"},
		"modelcontroller": {"base"},
		"sparkoperator":   {"overlays/odh"},
		"feastoperator":   {"overlays/odh"},
		"maas":            {"overlays/odh"},
	}

	for comp, expectedPaths := range verifyPaths {
		cp, ok := result[comp]
		if !ok {
			continue
		}
		for _, want := range expectedPaths {
			found := false
			for _, got := range cp.SourcePaths {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("component %q: expected path %q not found in %v", comp, want, cp.SourcePaths)
			}
		}
	}

	// Print what we found for visibility.
	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %s: %v", k, result[k].SourcePaths)
	}
}

func TestApplyScriptKeyOverrides(t *testing.T) {
	result := map[string]ComponentPaths{
		"modelsasservice": {Key: "modelsasservice", SourcePaths: []string{"overlays/odh"}},
		"dashboard":       {Key: "dashboard", SourcePaths: []string{"odh"}},
	}

	applyScriptKeyOverrides(result)

	if _, ok := result["modelsasservice"]; ok {
		t.Error("expected 'modelsasservice' key to be removed after override")
	}
	maas, ok := result["maas"]
	if !ok {
		t.Fatal("expected 'maas' key after override")
	}
	if maas.Key != "maas" {
		t.Errorf("expected Key = 'maas', got %q", maas.Key)
	}
	if _, ok := result["dashboard"]; !ok {
		t.Error("expected 'dashboard' to be unchanged")
	}
}

func TestExpandWorkbenches(t *testing.T) {
	result := map[string]ComponentPaths{
		"workbenches": {
			Key:         "workbenches",
			SourcePaths: []string{"base", "overlays/openshift", "odh/overlays/additional"},
			SourceFile:  "/test/workbenches",
		},
	}

	expandWorkbenches(result)

	if _, ok := result["workbenches"]; ok {
		t.Error("expected 'workbenches' key to be removed after expansion")
	}

	nbcPaths := result["workbenches/odh-notebook-controller"]
	if len(nbcPaths.SourcePaths) != 1 || nbcPaths.SourcePaths[0] != "base" {
		t.Errorf("odh-notebook-controller paths: got %v, want [base]", nbcPaths.SourcePaths)
	}

	kfPaths := result["workbenches/kf-notebook-controller"]
	if len(kfPaths.SourcePaths) != 1 || kfPaths.SourcePaths[0] != "overlays/openshift" {
		t.Errorf("kf-notebook-controller paths: got %v, want [overlays/openshift]", kfPaths.SourcePaths)
	}

	nbPaths := result["workbenches/notebooks"]
	if len(nbPaths.SourcePaths) != 1 || nbPaths.SourcePaths[0] != "odh/overlays/additional" {
		t.Errorf("notebooks paths: got %v, want [odh/overlays/additional]", nbPaths.SourcePaths)
	}
}

func TestExtractSourcePaths_IgnoresImageMaps(t *testing.T) {
	// imageParamMap and imagesMap should not produce false SourcePath matches.
	content := `
var imageParamMap = map[string]string{
	"odh-model-controller": "RELATED_IMAGE_ODH_MODEL_CONTROLLER_IMAGE",
}

func manifestsPath() types.ManifestInfo {
	return types.ManifestInfo{
		Path:       odhdeploy.DefaultManifestPath,
		ContextDir: ComponentName,
		SourcePath: "base",
	}
}
`
	paths := extractSourcePaths(content, "cluster.OpenDataHub")
	if len(paths) != 1 || paths[0] != "base" {
		t.Errorf("expected [base], got %v", paths)
	}
	for _, p := range paths {
		if strings.Contains(p, "RELATED_IMAGE") {
			t.Errorf("got spurious image path: %s", p)
		}
	}
}
