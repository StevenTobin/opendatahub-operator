package validate

// Platform identifies which set of manifests to validate.
type Platform string

const (
	PlatformODH   Platform = "ODH"
	PlatformRHOAI Platform = "RHOAI"
)

// ManifestEntry represents a single component from build/manifests-config.yaml.
type ManifestEntry struct {
	Key          string // destination directory, e.g. "dashboard", "workbenches/kf-notebook-controller"
	Org          string // GitHub org, e.g. "opendatahub-io" (from git.url)
	Repo         string // GitHub repo, e.g. "odh-dashboard" (from git.url)
	SourceFolder string // upstream source directory, e.g. "manifests", "config"
}

// ComponentPaths holds the SourcePath values for a single component,
// extracted from Go source code.
type ComponentPaths struct {
	Key         string   // matches ManifestEntry.Key
	SourcePaths []string // e.g. ["overlays/odh", "overlays/odh-xks"]
	SourceFile  string   // Go file where the paths were found
}

// Result is the outcome of validating one path for one component.
type Result struct {
	Component string `json:"component"`
	Path      string `json:"path"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error,omitempty"`
}

// Summary holds the aggregated outcome of a validation run.
type Summary struct {
	Platform Platform `json:"platform"`
	Results  []Result `json:"results"`
	Passed   int      `json:"passed"`
	Failed   int      `json:"failed"`
	Skipped  int      `json:"skipped"`
	Warnings []string `json:"warnings,omitempty"`
}

// Healthy returns true if no results failed.
func (s Summary) Healthy() bool {
	return s.Failed == 0
}
