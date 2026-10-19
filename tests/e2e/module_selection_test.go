package e2e_test

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules"
	modulebuiltin "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/builtin"
)

func selectedModuleRegistration() (modulebuiltin.ModuleRegistration, bool) {
	registration, focused, err := resolveModuleRegistration(testOpts.moduleConformanceSelection)
	if err != nil {
		return modulebuiltin.ModuleRegistration{}, false
	}
	return registration, focused
}

func validateModuleSelection(selection []string) error {
	_, _, err := resolveModuleRegistration(selection)
	return err
}

func resolveModuleRegistration(selection []string) (modulebuiltin.ModuleRegistration, bool, error) {
	names := make([]string, 0, len(selection))
	for _, value := range selection {
		for name := range strings.SplitSeq(value, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return modulebuiltin.ModuleRegistration{}, false, nil
	}
	if len(names) > 1 {
		return modulebuiltin.ModuleRegistration{}, false, fmt.Errorf("expected at most one module name, got %q", names)
	}

	for _, registration := range modulebuiltin.Registrations() {
		if registration.Handler.GetName() == names[0] {
			return registration, true, nil
		}
	}

	available := modulebuiltin.Names()
	slices.Sort(available)
	return modulebuiltin.ModuleRegistration{}, false, fmt.Errorf(
		"unknown module %q; available built-in modules: %s",
		names[0], strings.Join(available, ", "),
	)
}

func randomModuleRegistration(t *testing.T, source modules.ConfigSource) modulebuiltin.ModuleRegistration {
	t.Helper()

	if registration, focused := selectedModuleRegistration(); focused && registration.ConfigSource == source {
		t.Logf("Using selected %s module %q for focused coverage", configSourceName(source), registration.Handler.GetName())
		return registration
	}

	candidates := make([]modulebuiltin.ModuleRegistration, 0)
	for _, registration := range modulebuiltin.Registrations() {
		if registration.ConfigSource == source {
			candidates = append(candidates, registration)
		}
	}
	if len(candidates) == 0 {
		t.Fatalf("no built-in module registrations found for config source %d", source)
	}

	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
	if err != nil {
		t.Fatalf("choose random %s module: %v", configSourceName(source), err)
	}
	registration := candidates[index.Int64()]
	t.Logf("Selected random %s module %q for single-module coverage", configSourceName(source), registration.Handler.GetName())
	return registration
}

func TestResolveModuleRegistration(t *testing.T) {
	t.Parallel()

	all := modulebuiltin.Registrations()
	if len(all) == 0 {
		t.Fatal("built-in module registrations must not be empty")
	}

	t.Run("empty selection is full mode", func(t *testing.T) {
		t.Parallel()
		_, focused, err := resolveModuleRegistration([]string{"", "  "})
		if err != nil {
			t.Fatalf("resolve empty selection: %v", err)
		}
		if focused {
			t.Fatal("empty selection unexpectedly enabled focused mode")
		}
	})

	t.Run("valid selection returns production registration", func(t *testing.T) {
		t.Parallel()
		want := all[0]
		got, focused, err := resolveModuleRegistration([]string{"", " " + want.Handler.GetName() + " "})
		if err != nil {
			t.Fatalf("resolve valid selection: %v", err)
		}
		if !focused || got.Handler.GetName() != want.Handler.GetName() || got.ConfigSource != want.ConfigSource || got.Runlevel != want.Runlevel {
			t.Fatalf("resolved registration %#v, focused=%v; want %#v", got, focused, want)
		}
	})

	t.Run("unknown selection reports sorted available names", func(t *testing.T) {
		t.Parallel()
		_, focused, err := resolveModuleRegistration([]string{"does-not-exist"})
		if err == nil || focused || !strings.Contains(err.Error(), "available built-in modules") {
			t.Fatalf("unexpected unknown-module result: focused=%v error=%v", focused, err)
		}
	})

	t.Run("multiple selections are rejected", func(t *testing.T) {
		t.Parallel()
		_, focused, err := resolveModuleRegistration([]string{all[0].Handler.GetName(), all[1].Handler.GetName()})
		if err == nil || focused || !strings.Contains(err.Error(), "at most one") {
			t.Fatalf("unexpected multiple-module result: focused=%v error=%v", focused, err)
		}
	})
}

func moduleManagementField(registration modulebuiltin.ModuleRegistration, source modules.ConfigSource) string {
	if source != modules.ConfigFromDSC {
		return registration.Handler.GetName()
	}
	return dscComponentField(registration.Handler.GetName())
}
