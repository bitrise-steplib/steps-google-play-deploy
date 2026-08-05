//go:build manual_e2e

package main

// Manual producer→consumer e2e harness: drives the real pairing code against
// an artifact map ACTUALLY produced by a build step run. Not part of CI.
//
// Run:
//   E2E_MAP_PATH=/path/to/android-artifact-map.json \
//   E2E_APP_PATHS="/deploy/a.aab|/deploy/b.aab" \
//   E2E_MAPPING_FILE="/deploy/mapping.txt" \
//   go test -tags manual_e2e -run Test_ManualE2E -v .

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bitrise-io/go-utils/v2/log"
)

func Test_ManualE2E_PairingAgainstRealMap(t *testing.T) {
	mapPath := os.Getenv("E2E_MAP_PATH")
	appPaths := os.Getenv("E2E_APP_PATHS")
	if mapPath == "" || appPaths == "" {
		t.Skip("E2E_MAP_PATH / E2E_APP_PATHS not set")
	}
	logger := log.NewLogger()

	configs := Configs{
		AppPath:         appPaths,
		ArtifactMapPath: mapPath,
		MappingFile:     os.Getenv("E2E_MAPPING_FILE"),
		Logger:          logger,
	}

	pairing := newMappingPairing(configs, logger)
	if !pairing.usesArtifactMap() {
		t.Fatalf("expected the artifact map at %s to be loaded", mapPath)
	}

	apps, warnings := configs.appPaths()
	for _, w := range warnings {
		t.Logf("appPaths warning: %s", w)
	}
	if len(apps) == 0 {
		t.Fatal("no apps parsed from E2E_APP_PATHS")
	}

	paired := 0
	variantsByMapping := map[string]map[string]bool{}
	for i, app := range apps {
		mapping := pairing.mappingFor(i, app)
		t.Logf("app %-45s -> mapping %s", filepath.Base(app), mapping)
		if mapping == "" {
			continue
		}
		paired++
		if vm, known := pairing.lookup(app); known {
			if variantsByMapping[mapping] == nil {
				variantsByMapping[mapping] = map[string]bool{}
			}
			variantsByMapping[mapping][vm.variant] = true
		}
		if _, err := os.Stat(mapping); err != nil {
			t.Errorf("paired mapping does not exist: %v", err)
		}
		// each app's mapping must come from the map's dir (identity pairing),
		// not from the positional input
		if filepath.Dir(mapping) != filepath.Dir(mapPath) {
			t.Errorf("mapping %s is not from the artifact map's directory", mapping)
		}
	}
	if paired == 0 {
		t.Error("no artifact got a mapping paired — expected at least the release variants")
	}

	// Distinct variants must not share a mapping file (split APKs of ONE
	// variant sharing theirs is legitimate).
	for mapping, variants := range variantsByMapping {
		if len(variants) > 1 {
			t.Errorf("mapping %s shared by several variants: %v", mapping, variants)
		}
	}
}
