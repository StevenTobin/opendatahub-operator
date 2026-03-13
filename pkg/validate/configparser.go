package validate

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// manifestsConfigFile is the schema for build/manifests-config.yaml.
// Each entry maps a build-system component name to its source/destination.
type manifestsConfigFile struct {
	Map map[string]manifestConfigEntry `json:"map"`
}

type manifestConfigEntry struct {
	Src     string `json:"src"`
	Dest    string `json:"dest"`
	GitURL  string `json:"git.url"`
	RefType string `json:"ref_type"`
	Branch  string `json:"branch"`
}

// ParseManifestsConfig reads build/manifests-config.yaml and returns ManifestEntry
// values keyed by the destination directory (the same key used in get_all_manifests.sh
// and opt/manifests/<key>/).
func ParseManifestsConfig(path string) ([]ManifestEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifests config: %w", err)
	}
	return ParseManifestsConfigContent(data)
}

// ParseManifestsConfigContent parses the YAML content of manifests-config.yaml.
func ParseManifestsConfigContent(data []byte) ([]ManifestEntry, error) {
	var cfg manifestsConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing manifests config YAML: %w", err)
	}

	if len(cfg.Map) == 0 {
		return nil, errors.New("manifests config has no entries (missing 'map' key?)")
	}

	entries := make([]ManifestEntry, 0, len(cfg.Map))
	for _, entry := range cfg.Map {
		if entry.Dest == "" {
			continue
		}

		me := ManifestEntry{
			Key:          entry.Dest,
			SourceFolder: entry.Src,
		}

		if entry.GitURL != "" {
			me.Org, me.Repo = parseGitURL(entry.GitURL)
		}

		entries = append(entries, me)
	}

	if len(entries) == 0 {
		return nil, errors.New("manifests config produced no entries")
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	return entries, nil
}

// parseGitURL extracts org and repo from a GitHub HTTPS URL.
func parseGitURL(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}
