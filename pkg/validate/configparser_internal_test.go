package validate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestParseManifestsConfigContent_Basic(t *testing.T) {
	yamlContent := []byte(`map:
  odh-dashboard:
    src: manifests
    dest: dashboard
    git.url: https://github.com/opendatahub-io/odh-dashboard
  odh-kserve-controller:
    src: config
    dest: kserve
    git.url: https://github.com/opendatahub-io/kserve
  notebooks:
    src: manifests
    dest: workbenches/notebooks
    git.url: https://github.com/opendatahub-io/notebooks
    ref_type: branch
    branch: stable
`)

	entries, err := ParseManifestsConfigContent(yamlContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	byKey := make(map[string]ManifestEntry)
	for _, e := range entries {
		byKey[e.Key] = e
	}

	dash, ok := byKey["dashboard"]
	if !ok {
		t.Fatal("missing dashboard entry")
	}
	if dash.SourceFolder != "manifests" {
		t.Errorf("dashboard.SourceFolder = %q, want %q", dash.SourceFolder, "manifests")
	}
	if dash.Org != "opendatahub-io" {
		t.Errorf("dashboard.Org = %q, want %q", dash.Org, "opendatahub-io")
	}
	if dash.Repo != "odh-dashboard" {
		t.Errorf("dashboard.Repo = %q, want %q", dash.Repo, "odh-dashboard")
	}

	wb, ok := byKey["workbenches/notebooks"]
	if !ok {
		t.Fatal("missing workbenches/notebooks entry")
	}
	if wb.SourceFolder != "manifests" {
		t.Errorf("workbenches/notebooks.SourceFolder = %q, want %q", wb.SourceFolder, "manifests")
	}
}

func TestParseManifestsConfigContent_EmptyMap(t *testing.T) {
	yamlContent := []byte(`map: {}`)
	_, err := ParseManifestsConfigContent(yamlContent)
	if err == nil {
		t.Fatal("expected error for empty map, got nil")
	}
}

func TestParseManifestsConfigContent_MissingMapKey(t *testing.T) {
	yamlContent := []byte(`something_else: true`)
	_, err := ParseManifestsConfigContent(yamlContent)
	if err == nil {
		t.Fatal("expected error for missing map key, got nil")
	}
}

func TestParseManifestsConfigContent_NoGitURL(t *testing.T) {
	yamlContent := []byte(`map:
  simple:
    src: config
    dest: mycomp
`)
	entries, err := ParseManifestsConfigContent(yamlContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Org != "" || entries[0].Repo != "" {
		t.Errorf("expected empty Org/Repo when no git.url, got %q/%q", entries[0].Org, entries[0].Repo)
	}
}

func TestParseGitURL(t *testing.T) {
	tests := []struct {
		url      string
		wantOrg  string
		wantRepo string
	}{
		{"https://github.com/opendatahub-io/odh-dashboard", "opendatahub-io", "odh-dashboard"},
		{"https://github.com/red-hat-data-services/kserve", "red-hat-data-services", "kserve"},
		{"invalid-url", "", ""},
	}

	for _, tt := range tests {
		org, repo := parseGitURL(tt.url)
		if org != tt.wantOrg || repo != tt.wantRepo {
			t.Errorf("parseGitURL(%q) = (%q, %q), want (%q, %q)", tt.url, org, repo, tt.wantOrg, tt.wantRepo)
		}
	}
}

func TestParseManifestsConfig_RealFile(t *testing.T) {
	realPath := filepath.Join("..", "..", "build", "manifests-config.yaml")
	if _, err := os.Stat(realPath); os.IsNotExist(err) {
		t.Skip("build/manifests-config.yaml not found at expected relative path, skipping")
	}

	entries, err := ParseManifestsConfig(realPath)
	if err != nil {
		t.Fatalf("failed to parse real manifests config: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("expected at least one entry from real manifests config")
	}

	found := make(map[string]bool)
	for _, e := range entries {
		found[e.Key] = true
	}

	for _, want := range []string{"dashboard", "kserve", "ray", "trustyai", "maas", "sparkoperator"} {
		if !found[want] {
			t.Errorf("expected to find component %q in parsed entries", want)
		}
	}

	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %s", k)
	}
}
