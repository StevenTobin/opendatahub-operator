package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/validate"
)

// preflightManifestValidation checks that all manifest SourcePath sub-directories
// referenced by the operator exist in the locally downloaded manifests
// (opt/manifests/). This runs as the very first step before any e2e tests to
// fail fast with a clear error.
//
// If opt/manifests/ does not exist, get_all_manifests.sh is run automatically
// to fetch them before validating.
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
		fetchManifests(t, repoRoot)
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

// fetchManifests runs get_all_manifests.sh to download component manifests.
func fetchManifests(t *testing.T, repoRoot string) {
	t.Helper()

	script := filepath.Join(repoRoot, "get_all_manifests.sh")
	if _, err := os.Stat(script); os.IsNotExist(err) {
		t.Fatalf("get_all_manifests.sh not found at %s", script)
	}

	t.Log("opt/manifests/ not found, running get_all_manifests.sh...")

	cmd := exec.CommandContext(context.Background(), script)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("get_all_manifests.sh failed: %v", err)
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
