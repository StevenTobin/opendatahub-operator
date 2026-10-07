package e2e_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opendatahub-io/opendatahub-operator/pkg/scoperules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
)

// e2eScopeRulesRelPath is scoperules.DefaultPath made relative to this
// package's own directory, since `go test` runs with cwd set to
// tests/e2e, not the repo root.
var e2eScopeRulesRelPath = strings.TrimPrefix(scoperules.DefaultPath, "tests/e2e/")

func loadMinimalScopeRules(t *testing.T) *scoperules.Rules {
	t.Helper()

	rules, err := scoperules.Load(e2eScopeRulesRelPath)
	require.NoError(t, err, "loading %s", e2eScopeRulesRelPath)

	return rules
}

// TestScopeRulesModulesMatchBuiltinRegistry keeps selective E2E attribution in
// sync with the production module registry used by the DAG contract suite.
func TestScopeRulesModulesMatchBuiltinRegistry(t *testing.T) {
	rules := loadMinimalScopeRules(t)

	realNames := map[string]bool{}
	for _, name := range modulebuiltin.Names() {
		realNames[name] = true
	}

	for name, entry := range rules.Components {
		if !entry.IsCovered() {
			continue
		}
		if !realNames[name] {
			t.Errorf("e2e-scope-rules.yaml components.%s is selectable but %q is not a registered module", name, name)
		}
	}

	for _, name := range modulebuiltin.Names() {
		entry, hasEntry := rules.Components[name]
		if !hasEntry {
			t.Errorf("registered module %q has no components.%s entry in e2e-scope-rules.yaml", name, name)
			continue
		}
		if !entry.IsCovered() {
			t.Errorf("registered module %q is marked uncovered even though every module participates in the DAG contract suite", name)
		}
	}
}

// TestScopeRulesServicesMatchRealTestGroup is the services-side equivalent
// of TestScopeRulesComponentsMatchRealTestGroup.
func TestScopeRulesServicesMatchRealTestGroup(t *testing.T) {
	rules := loadMinimalScopeRules(t)

	realNames := map[string]bool{}
	for _, n := range Services.Names() {
		realNames[n] = true
	}

	for name, entry := range rules.Services {
		if !entry.IsCovered() {
			continue
		}
		if !realNames[name] {
			t.Errorf("e2e-scope-rules.yaml services.%s is selectable but %q is not a real name in tests/e2e's Services TestGroup -- TestGroup.Validate() would reject it", name, name)
		}
	}

	deps := scoperules.DepsTargets(rules.Services)
	for _, name := range Services.Names() {
		_, hasEntry := rules.Services[name]
		if !hasEntry && !deps[name] {
			t.Errorf("tests/e2e's Services TestGroup has %q but e2e-scope-rules.yaml has no services.%s entry "+
				"and no entry depends on it -- the resolver can never select it, forcing a full-suite run for any change to it", name, name)
		}
	}
}
