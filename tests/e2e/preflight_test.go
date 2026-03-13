package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/validate"
)

// preflightManifestValidation checks that all manifest SourcePath sub-directories
// referenced by the operator exist in the locally downloaded manifests
// (opt/manifests/). This runs as the very first step before any e2e tests to
// fail fast with a clear error.
//
// Prerequisite: get_all_manifests.sh must have been run first.
func preflightManifestValidation(t *testing.T) {
	t.Helper()

	repoRoot := findRepoRoot(t)
	configPath := filepath.Join(repoRoot, "build", "manifests-config.yaml")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Skipf("build/manifests-config.yaml not found at %s; skipping preflight manifest validation", configPath)
		return
	}

	manifestsDir := filepath.Join(repoRoot, "opt", "manifests")
	if _, err := os.Stat(manifestsDir); os.IsNotExist(err) {
		t.Fatalf("opt/manifests/ directory not found -- run get_all_manifests.sh before e2e tests")
	}

	summary, err := validate.ValidateManifests(validate.Config{
		ConfigPath: configPath,
		RepoRoot:   repoRoot,
		Platform:   validate.PlatformODH,
	})
	if err != nil {
		t.Fatalf("Preflight manifest validation failed to run: %v", err)
	}

	t.Log(validate.FormatSummary(summary))

	if !summary.Healthy() {
		t.Fatalf("Preflight manifest validation FAILED: %d components have invalid paths. "+
			"Fix the manifest references before running e2e tests.", summary.Failed)
	}
}

// findRepoRoot walks up from the working directory to find the repo root
// (identified by the presence of go.mod).
func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot determine working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("cannot find repo root (go.mod) from %s", dir)
		}
		dir = parent
	}
}
