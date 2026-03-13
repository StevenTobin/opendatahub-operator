package validate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultManifestsDir = "opt/manifests"
	defaultChartsDir    = "opt/charts"
)

// Config holds all inputs for a manifest validation run.
type Config struct {
	ConfigPath string   // path to build/manifests-config.yaml
	RepoRoot   string   // path to the operator repo root (for Go source parsing + local manifests)
	Platform   Platform // ODH or RHOAI
}

// ValidateManifests reads the manifests config YAML and component source paths,
// then verifies that the expected SourcePath sub-directories exist in the
// locally downloaded manifests under opt/manifests/.
//
// Prerequisite: get_all_manifests.sh must have been run first.
func ValidateManifests(cfg Config) (*Summary, error) {
	entries, err := ParseManifestsConfig(cfg.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("parsing manifests config: %w", err)
	}

	componentPaths, err := ParseComponentSourcePaths(cfg.RepoRoot, cfg.Platform)
	if err != nil {
		return nil, fmt.Errorf("parsing component source paths: %w", err)
	}

	return RunValidation(cfg.RepoRoot, entries, componentPaths, cfg.Platform)
}

// RunValidation checks that each component's SourcePath sub-directories
// exist within the locally downloaded manifests (opt/manifests/<key>/<SourcePath>).
//
// The entries list is platform-agnostic (from manifests-config.yaml). Only
// components that are locally present (downloaded by get_all_manifests.sh for
// the target platform) are validated. Components not present locally are
// skipped unless the Go source defines paths for them on this platform, in
// which case they are reported as failures.
func RunValidation(repoRoot string, entries []ManifestEntry, componentPaths map[string]ComponentPaths, platform Platform) (*Summary, error) {
	summary := &Summary{Platform: platform}

	manifestsDir := filepath.Join(repoRoot, defaultManifestsDir)
	chartsDir := filepath.Join(repoRoot, defaultChartsDir)

	entryMap := make(map[string]ManifestEntry, len(entries))
	for _, e := range entries {
		entryMap[e.Key] = e
	}

	for _, entry := range entries {
		baseDir := manifestsDir
		if _, err := os.Stat(filepath.Join(chartsDir, entry.Key)); err == nil {
			baseDir = chartsDir
		}

		componentDir := filepath.Join(baseDir, entry.Key)
		cp, hasPaths := componentPaths[entry.Key]

		if _, err := os.Stat(componentDir); os.IsNotExist(err) {
			if hasPaths {
				summary.Results = append(summary.Results, Result{
					Component: entry.Key,
					Path:      componentDir,
					Error: fmt.Sprintf("component directory not found but Go source defines paths %v -- "+
						"has get_all_manifests.sh been run?", cp.SourcePaths),
				})
				summary.Failed++
			} else {
				summary.Skipped++
			}
			continue
		}

		if !hasPaths {
			summary.Warnings = append(summary.Warnings,
				fmt.Sprintf("%s: no source paths found in Go code (may use dynamic paths)", entry.Key))
			summary.Results = append(summary.Results, Result{
				Component: entry.Key,
				Path:      componentDir,
				Valid:     true,
			})
			summary.Passed++
			continue
		}

		allPathsOK := true
		for _, sp := range cp.SourcePaths {
			fullPath := filepath.Join(componentDir, sp)
			info, err := os.Stat(fullPath)
			if os.IsNotExist(err) {
				summary.Results = append(summary.Results, Result{
					Component: entry.Key,
					Path:      filepath.Join(entry.Key, sp),
					Error: fmt.Sprintf("path '%s' not found in downloaded manifests for %s",
						sp, entry.Key),
				})
				allPathsOK = false
				continue
			}
			if err != nil {
				summary.Results = append(summary.Results, Result{
					Component: entry.Key,
					Path:      filepath.Join(entry.Key, sp),
					Error:     fmt.Sprintf("error checking path '%s': %v", sp, err),
				})
				allPathsOK = false
				continue
			}
			if !info.IsDir() {
				summary.Results = append(summary.Results, Result{
					Component: entry.Key,
					Path:      filepath.Join(entry.Key, sp),
					Error:     fmt.Sprintf("'%s' exists but is not a directory", sp),
				})
				allPathsOK = false
				continue
			}

			summary.Results = append(summary.Results, Result{
				Component: entry.Key,
				Path:      filepath.Join(entry.Key, sp),
				Valid:     true,
			})
		}

		if allPathsOK {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}

	for key := range componentPaths {
		if _, ok := entryMap[key]; !ok {
			summary.Warnings = append(summary.Warnings,
				fmt.Sprintf("%s: found in Go source but has no entry in manifests config", key))
		}
	}

	sort.Strings(summary.Warnings)

	return summary, nil
}

// FormatSummary returns a human-readable string of the validation results.
func FormatSummary(s *Summary) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Manifest path validation (%s)\n\n", s.Platform)

	type componentResult struct {
		passes []Result
		fails  []Result
	}
	grouped := make(map[string]*componentResult)
	var order []string
	for _, r := range s.Results {
		cr, ok := grouped[r.Component]
		if !ok {
			cr = &componentResult{}
			grouped[r.Component] = cr
			order = append(order, r.Component)
		}
		if r.Valid {
			cr.passes = append(cr.passes, r)
		} else {
			cr.fails = append(cr.fails, r)
		}
	}

	for _, comp := range order {
		cr := grouped[comp]
		if len(cr.fails) == 0 {
			fmt.Fprintf(&b, "[PASS] %-35s", comp)
			var paths []string
			for _, r := range cr.passes {
				paths = append(paths, r.Path)
			}
			fmt.Fprintf(&b, "  %s\n", strings.Join(paths, ", "))
		} else {
			fmt.Fprintf(&b, "[FAIL] %-35s\n", comp)
			for _, r := range cr.fails {
				fmt.Fprintf(&b, "       %s\n", r.Error)
			}
		}
	}

	if len(s.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings:\n")
		for _, w := range s.Warnings {
			fmt.Fprintf(&b, "  %s\n", w)
		}
	}

	fmt.Fprintf(&b, "\n%d passed, %d failed", s.Passed, s.Failed)
	if s.Skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped (not downloaded for this platform)", s.Skipped)
	}
	fmt.Fprintln(&b)

	return b.String()
}

// FormatJSON writes the summary as JSON to the given writer.
func FormatJSON(w io.Writer, s *Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
