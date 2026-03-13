package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateManifests_AllPass(t *testing.T) {
	tmpDir := t.TempDir()

	writeYAMLConfig(t, tmpDir, `map:
  odh-dashboard:
    src: manifests
    dest: dashboard
  odh-kserve-controller:
    src: config
    dest: kserve
`)

	compDir := filepath.Join(tmpDir, "internal", "controller", "components", "dashboard")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(compDir, "support.go"), `package dashboard
import (
	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	componentApi "github.com/opendatahub-io/opendatahub-operator/v2/api/components/v1alpha1"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
)
const ComponentName = componentApi.DashboardComponentName
var overlaysSourcePaths = map[common.Platform]string{
	cluster.OpenDataHub: "odh",
}
`)

	kserveDir := filepath.Join(tmpDir, "internal", "controller", "components", "kserve")
	if err := os.MkdirAll(kserveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(kserveDir, "kserve.go"), `package kserve
import componentApi "github.com/opendatahub-io/opendatahub-operator/v2/api/components/v1alpha1"
const componentName = componentApi.KserveComponentName
const kserveManifestSourcePath = "overlays/odh"
`)

	for _, p := range []string{
		"opt/manifests/dashboard/odh",
		"opt/manifests/kserve/overlays/odh",
	} {
		if err := os.MkdirAll(filepath.Join(tmpDir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := ValidateManifests(Config{
		ConfigPath: filepath.Join(tmpDir, "manifests-config.yaml"),
		RepoRoot:   tmpDir,
		Platform:   PlatformODH,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.Failed != 0 {
		t.Errorf("expected 0 failures, got %d\n%s", summary.Failed, FormatSummary(summary))
	}
	if summary.Passed != 2 {
		t.Errorf("expected 2 passed, got %d", summary.Passed)
	}
}

func TestValidateManifests_MissingSourcePath(t *testing.T) {
	tmpDir := t.TempDir()

	writeYAMLConfig(t, tmpDir, `map:
  odh-kserve-controller:
    src: config
    dest: kserve
`)

	kserveDir := filepath.Join(tmpDir, "internal", "controller", "components", "kserve")
	if err := os.MkdirAll(kserveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(kserveDir, "kserve.go"), `package kserve
import componentApi "github.com/opendatahub-io/opendatahub-operator/v2/api/components/v1alpha1"
const componentName = componentApi.KserveComponentName
const kserveManifestSourcePath = "overlays/odh"
const kserveManifestSourcePathXKS = "overlays/odh-xks"
`)

	// Only create one of the two expected paths.
	if err := os.MkdirAll(filepath.Join(tmpDir, "opt/manifests/kserve/overlays/odh"), 0o755); err != nil {
		t.Fatal(err)
	}

	summary, err := ValidateManifests(Config{
		ConfigPath: filepath.Join(tmpDir, "manifests-config.yaml"),
		RepoRoot:   tmpDir,
		Platform:   PlatformODH,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.Failed != 1 {
		t.Errorf("expected 1 failure, got %d\n%s", summary.Failed, FormatSummary(summary))
	}

	output := FormatSummary(summary)
	if !strings.Contains(output, "[FAIL]") {
		t.Errorf("expected [FAIL] in output:\n%s", output)
	}
	if !strings.Contains(output, "overlays/odh-xks") {
		t.Errorf("expected overlays/odh-xks in failure output:\n%s", output)
	}
}

func TestValidateManifests_NotDownloaded(t *testing.T) {
	tmpDir := t.TempDir()

	writeYAMLConfig(t, tmpDir, `map:
  odh-dashboard:
    src: manifests
    dest: dashboard
`)

	dashDir := filepath.Join(tmpDir, "internal", "controller", "components", "dashboard")
	if err := os.MkdirAll(dashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dashDir, "support.go"), `package dashboard
import componentApi "github.com/opendatahub-io/opendatahub-operator/v2/api/components/v1alpha1"
const ComponentName = componentApi.DashboardComponentName
func m() { _ = struct{ SourcePath string }{SourcePath: "odh"} }
`)

	summary, err := ValidateManifests(Config{
		ConfigPath: filepath.Join(tmpDir, "manifests-config.yaml"),
		RepoRoot:   tmpDir,
		Platform:   PlatformODH,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.Failed != 1 {
		t.Errorf("expected 1 failure, got %d\n%s", summary.Failed, FormatSummary(summary))
	}

	output := FormatSummary(summary)
	if !strings.Contains(output, "get_all_manifests.sh") {
		t.Errorf("expected helpful hint about running the script:\n%s", output)
	}
}

func TestValidateManifests_SkipsComponentNotForPlatform(t *testing.T) {
	tmpDir := t.TempDir()

	writeYAMLConfig(t, tmpDir, `map:
  odh-dashboard:
    src: manifests
    dest: dashboard
  odh-extra:
    src: config
    dest: extra
`)

	dashDir := filepath.Join(tmpDir, "internal", "controller", "components", "dashboard")
	if err := os.MkdirAll(dashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dashDir, "support.go"), `package dashboard
import (
	"github.com/opendatahub-io/opendatahub-operator/v2/api/common"
	componentApi "github.com/opendatahub-io/opendatahub-operator/v2/api/components/v1alpha1"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
)
const ComponentName = componentApi.DashboardComponentName
var overlaysSourcePaths = map[common.Platform]string{
	cluster.OpenDataHub: "odh",
}
`)

	// Only dashboard is downloaded locally; "extra" isn't for this platform
	// and has no component parser entry, so it should be silently skipped.
	if err := os.MkdirAll(filepath.Join(tmpDir, "opt/manifests/dashboard/odh"), 0o755); err != nil {
		t.Fatal(err)
	}

	summary, err := ValidateManifests(Config{
		ConfigPath: filepath.Join(tmpDir, "manifests-config.yaml"),
		RepoRoot:   tmpDir,
		Platform:   PlatformODH,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.Failed != 0 {
		t.Errorf("expected 0 failures, got %d\n%s", summary.Failed, FormatSummary(summary))
	}
	if summary.Passed != 1 {
		t.Errorf("expected 1 passed, got %d", summary.Passed)
	}
	if summary.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", summary.Skipped)
	}
}

func TestFormatJSON(t *testing.T) {
	s := &Summary{
		Platform: PlatformODH,
		Results: []Result{
			{Component: "dashboard", Path: "dashboard/odh", Valid: true},
			{Component: "kserve", Path: "kserve/overlays/bad", Valid: false, Error: "not found"},
		},
		Passed: 1,
		Failed: 1,
	}

	var buf bytes.Buffer
	if err := FormatJSON(&buf, s); err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"component"`) || !strings.Contains(output, `"not found"`) {
		t.Errorf("unexpected JSON output:\n%s", output)
	}
}

func writeYAMLConfig(t *testing.T, tmpDir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(tmpDir, "manifests-config.yaml"), content)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
