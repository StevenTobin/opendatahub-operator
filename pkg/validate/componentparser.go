package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// platformConstant maps the Go cluster constant to our Platform type.
var platformConstant = map[Platform]string{
	PlatformODH:   "cluster.OpenDataHub",
	PlatformRHOAI: "cluster.SelfManagedRhoai",
}

// platformMapEntry matches `cluster.OpenDataHub: "overlays/odh"` style entries
// in map[common.Platform]string literals.
var platformMapEntry = regexp.MustCompile(
	`cluster\.\w+:\s*"([^"]*)"`,
)

// pathConstPattern matches const/var declarations whose name contains path-related
// keywords and whose value looks like a filesystem path.
// e.g. `kserveManifestSourcePath    = "overlays/odh"`
// e.g. `kserveManifestSourcePathXKS = "overlays/odh-xks"`
// e.g. `BaseManifestsSourcePath     = "overlays/odh"`.
var pathConstPattern = regexp.MustCompile(
	`(?i)\w*(?:manifest|source|overlay)\w*path\w*\s*=\s*"([^"]*)"`,
)

// pathLikeVarNames are substrings in map variable names that indicate a path map.
var pathLikeVarNames = []string{
	"SourcePath", "sourcePath", "ManifestPath", "manifestPath",
	"overlays", "Overlays",
}

// sourcePathLiteral matches `SourcePath: "some/path"` in ManifestInfo struct literals.
var sourcePathLiteral = regexp.MustCompile(
	`SourcePath:\s*"([^"]*)"`,
)

// componentNameConst matches `ComponentName = componentApi.XxxComponentName` or `ComponentName = "xxx"`.
var componentNameConst = regexp.MustCompile(
	`(?:componentName|ComponentName)\s*=\s*(?:componentApi\.(\w+ComponentName)|"([^"]*)")`,
)

// componentNameMap maps known componentApi constant names to the actual string values
// used as destination keys in manifests-config.yaml.
var componentNameMap = map[string]string{
	"DashboardComponentName":            "dashboard",
	"KserveComponentName":               "kserve",
	"RayComponentName":                  "ray",
	"TrustyAIComponentName":             "trustyai",
	"ModelRegistryComponentName":        "modelregistry",
	"ModelControllerComponentName":      "modelcontroller",
	"SparkOperatorComponentName":        "sparkoperator",
	"DataSciencePipelinesComponentName": "datasciencepipelines",
	"TrainingOperatorComponentName":     "trainingoperator",
	"TrainerComponentName":              "trainer",
	"FeastOperatorComponentName":        "feastoperator",
	"LlamaStackOperatorComponentName":   "llamastackoperator",
	"MLflowOperatorComponentName":       "mlflowoperator",
	"ModelsAsServiceComponentName":      "modelsasservice",
	"WorkbenchesComponentName":          "workbenches",
	"KueueComponentName":                "kueue",
}

// scriptKeyOverrides maps ComponentName values to manifests-config.yaml destination
// keys when they differ. Most components have ComponentName == dest key, but
// a few diverge (e.g. MaaS uses ComponentName "modelsasservice" but dest key "maas").
//
// MAINTAINER: if you add a new component where the ComponentName doesn't match
// the dest key in manifests-config.yaml, add the mapping here.
var scriptKeyOverrides = map[string]string{
	"modelsasservice": "maas",
}

// ParseComponentSourcePaths scans Go source files under the given directory tree
// to extract SourcePath values for each component. It returns a map from
// component key (matching manifests-config.yaml dest values) to ComponentPaths.
func ParseComponentSourcePaths(rootDir string, platform Platform) (map[string]ComponentPaths, error) {
	platformKey, ok := platformConstant[platform]
	if !ok {
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}

	result := make(map[string]ComponentPaths)

	componentsDir := filepath.Join(rootDir, "internal", "controller", "components")
	if err := scanComponentDirs(componentsDir, platformKey, result); err != nil {
		return nil, fmt.Errorf("scanning components: %w", err)
	}

	servicesDir := filepath.Join(rootDir, "internal", "controller", "services")
	if err := scanComponentDirs(servicesDir, platformKey, result); err != nil {
		return nil, fmt.Errorf("scanning services: %w", err)
	}

	// Expand workbenches sub-components. The workbenches component uses multiple
	// ContextDirs that map to separate manifests-config.yaml dest keys.
	expandWorkbenches(result)

	// Remap component keys where ComponentName != manifests-config.yaml dest key.
	applyScriptKeyOverrides(result)

	return result, nil
}

// scanComponentDirs walks each subdirectory in dir, reading all .go files
// to extract component name and source paths.
func scanComponentDirs(dir string, platformKey string, result map[string]ComponentPaths) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		componentDir := filepath.Join(dir, entry.Name())
		paths, err := parseComponentDir(componentDir, platformKey)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}
		if paths != nil {
			result[paths.Key] = *paths
		}
	}

	return nil
}

// parseComponentDir reads all .go files in a component directory and extracts
// the component key and source paths.
func parseComponentDir(dir string, platformKey string) (*ComponentPaths, error) {
	goFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}

	var allContent strings.Builder

	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		allContent.Write(data)
		allContent.WriteByte('\n')
	}

	content := allContent.String()

	componentKey := extractComponentKey(content)
	if componentKey == "" {
		return nil, nil
	}

	sourcePaths := extractSourcePaths(content, platformKey)
	if len(sourcePaths) == 0 {
		return nil, nil
	}

	return &ComponentPaths{
		Key:         componentKey,
		SourcePaths: sourcePaths,
		SourceFile:  dir,
	}, nil
}

// extractComponentKey finds the ComponentName constant and maps it to a
// manifests-config.yaml destination key.
func extractComponentKey(content string) string {
	matches := componentNameConst.FindStringSubmatch(content)
	if matches == nil {
		return ""
	}

	// Direct string value: ComponentName = "xxx"
	if matches[2] != "" {
		return matches[2]
	}

	// API constant reference: ComponentName = componentApi.XxxComponentName
	if apiConst := matches[1]; apiConst != "" {
		if key, ok := componentNameMap[apiConst]; ok {
			return key
		}
		// Unknown API constant -- likely a new component that needs to be
		// added to componentNameMap in componentparser.go.
		fmt.Fprintf(os.Stderr, "validate: unknown component API constant %q -- add it to componentNameMap in pkg/validate/componentparser.go\n", apiConst)
	}

	return ""
}

// extractSourcePaths finds all SourcePath values from the Go source for a given platform.
func extractSourcePaths(content string, platformKey string) []string {
	seen := make(map[string]bool)
	var paths []string

	addPath := func(p string) {
		p = strings.TrimPrefix(p, "/")
		if p == "" || seen[p] {
			return
		}
		// Filter out values that don't look like filesystem paths.
		if strings.Contains(p, " ") || strings.HasPrefix(p, "RELATED_IMAGE") {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	// Strategy 1: Extract values from path-related platform maps for the target platform.
	extractPlatformMapValues(content, platformKey, addPath)

	// Strategy 2: Extract direct SourcePath string literals from ManifestInfo structs.
	for _, m := range sourcePathLiteral.FindAllStringSubmatch(content, -1) {
		addPath(m[1])
	}

	// Strategy 3: Extract const/var declarations that look like manifest source paths.
	// Catches patterns like `kserveManifestSourcePath = "overlays/odh"`.
	for _, m := range pathConstPattern.FindAllStringSubmatch(content, -1) {
		addPath(m[1])
	}

	return paths
}

// platformMapBlockWithName captures the variable name and body of map[common.Platform]string literals.
var platformMapBlockWithName = regexp.MustCompile(
	`(?s)(\w+)\s*=\s*map\[common\.Platform\]string\{(.*?)\}`,
)

// extractPlatformMapValues finds path-related map[common.Platform]string{...} blocks,
// then extracts the value for the target platform key. Non-path maps (like sectionTitle)
// are skipped.
func extractPlatformMapValues(content string, platformKey string, addPath func(string)) {
	blocks := platformMapBlockWithName.FindAllStringSubmatch(content, -1)
	for _, block := range blocks {
		varName := block[1]

		if !isPathMapName(varName) {
			continue
		}

		body := block[2]
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, platformKey) {
				if m := platformMapEntry.FindStringSubmatch(line); m != nil {
					addPath(m[1])
				}
			}
		}
	}
}

// isPathMapName returns true if the variable name looks like it holds manifest paths.
func isPathMapName(name string) bool {
	for _, substr := range pathLikeVarNames {
		if strings.Contains(name, substr) {
			return true
		}
	}
	return false
}

// expandWorkbenches handles the special case where the "workbenches" component
// uses multiple ContextDirs that map to separate manifests-config.yaml dest keys.
func expandWorkbenches(result map[string]ComponentPaths) {
	wb, ok := result["workbenches"]
	if !ok {
		return
	}

	// The workbenches component defines sub-paths mapped to different manifest keys.
	// We need to split the discovered paths into the correct sub-component keys.
	subComponents := map[string][]string{
		"workbenches/odh-notebook-controller": {"base"},
		"workbenches/kf-notebook-controller":  {"overlays/openshift"},
		"workbenches/notebooks":               nil, // collect remaining paths
	}

	assigned := make(map[string]bool)
	for key, expectedPaths := range subComponents {
		if expectedPaths == nil {
			continue
		}
		for _, p := range expectedPaths {
			assigned[p] = true
		}
		result[key] = ComponentPaths{
			Key:         key,
			SourcePaths: expectedPaths,
			SourceFile:  wb.SourceFile,
		}
	}

	// Remaining paths go to the notebooks sub-component.
	var notebookPaths []string
	for _, p := range wb.SourcePaths {
		if !assigned[p] {
			notebookPaths = append(notebookPaths, p)
		}
	}
	if len(notebookPaths) > 0 {
		result["workbenches/notebooks"] = ComponentPaths{
			Key:         "workbenches/notebooks",
			SourcePaths: notebookPaths,
			SourceFile:  wb.SourceFile,
		}
	}

	delete(result, "workbenches")
}

// applyScriptKeyOverrides renames component keys in the result map where
// the ComponentName differs from the manifests-config.yaml dest key.
func applyScriptKeyOverrides(result map[string]ComponentPaths) {
	for oldKey, newKey := range scriptKeyOverrides {
		if cp, ok := result[oldKey]; ok {
			cp.Key = newKey
			result[newKey] = cp
			delete(result, oldKey)
		}
	}
}
