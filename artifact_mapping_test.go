package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testArtifactMap = `{
  "version": 1,
  "variants": {
    "demoRelease": {
      "module": "app",
      "mapping": "mapping.txt",
      "aab": ["app-demo-release.aab"],
      "apk": ["app-demo-release.apk"]
    },
    "paidRelease": {
      "module": "app",
      "mapping": "mapping-20260805121530.txt",
      "aab": ["app-paid-release.aab"],
      "apk": []
    },
    "freeDebug": {
      "module": "app",
      "aab": [],
      "apk": ["app-free-debug.apk"]
    }
  },
  "unmatched": { "apk": [], "aab": [], "mapping": [] }
}`

// writeTestArtifactMap writes the fixture map into a temp deploy dir and
// returns the dir and the map's path.
func writeTestArtifactMap(t *testing.T) (string, string) {
	t.Helper()
	deployDir := t.TempDir()
	mapPath := filepath.Join(deployDir, "android-artifact-map.json")
	require.NoError(t, os.WriteFile(mapPath, []byte(testArtifactMap), 0600))
	return deployDir, mapPath
}

func testConfigs(mapPath, mappingFile string) Configs {
	return Configs{
		ArtifactMapPath: mapPath,
		MappingFile:     mappingFile,
		Logger:          log.NewLogger(),
	}
}

func Test_mappingFor_MapHit_UsesTheVariantsMapping(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing, err := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())
	require.NoError(t, err)

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-paid-release.aab"), log.NewLogger())

	assert.Equal(t, filepath.Join(deployDir, "mapping-20260805121530.txt"), got)
}

func Test_mappingFor_MapWinsOverPositionalInput(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing, err := newMappingPairing(testConfigs(mapPath, "/somewhere/else/mapping.txt"), log.NewLogger())
	require.NoError(t, err)

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-demo-release.aab"), log.NewLogger())

	assert.Equal(t, filepath.Join(deployDir, "mapping.txt"), got)
}

func Test_mappingFor_KnownVariantWithoutMapping_UploadsNothing(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	// even though the positional input would provide a file for this index
	pairing, err := newMappingPairing(testConfigs(mapPath, "/somewhere/else/mapping.txt"), log.NewLogger())
	require.NoError(t, err)

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-free-debug.apk"), log.NewLogger())

	assert.Equal(t, "", got)
}

func Test_mappingFor_UnknownApp_FallsBackToPositional(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing, err := newMappingPairing(testConfigs(mapPath, "first-mapping.txt|second-mapping.txt"), log.NewLogger())
	require.NoError(t, err)

	got := pairing.mappingFor(1, filepath.Join(deployDir, "hand-built.aab"), log.NewLogger())

	assert.Equal(t, "second-mapping.txt", got)
}

func Test_mappingFor_NoMapConfigured_KeepsPositionalBehavior(t *testing.T) {
	pairing, err := newMappingPairing(testConfigs("", "a.txt|b.txt"), log.NewLogger())
	require.NoError(t, err)

	assert.Equal(t, "a.txt", pairing.mappingFor(0, "/deploy/app1.aab", log.NewLogger()))
	assert.Equal(t, "b.txt", pairing.mappingFor(1, "/deploy/app2.aab", log.NewLogger()))
	assert.Equal(t, "", pairing.mappingFor(2, "/deploy/app3.aab", log.NewLogger()))
}

func Test_mappingFor_MissingMapFile_KeepsPositionalBehavior(t *testing.T) {
	pairing, err := newMappingPairing(testConfigs("/nonexistent/android-artifact-map.json", "a.txt"), log.NewLogger())
	require.NoError(t, err)

	assert.False(t, pairing.usesArtifactMap())
	assert.Equal(t, "a.txt", pairing.mappingFor(0, "/deploy/app1.aab", log.NewLogger()))
}

func Test_newMappingPairing_InvalidMapFileIsAnError(t *testing.T) {
	deployDir := t.TempDir()
	mapPath := filepath.Join(deployDir, "android-artifact-map.json")
	require.NoError(t, os.WriteFile(mapPath, []byte(`{"not": "an artifact map"}`), 0600))

	_, err := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())

	assert.Error(t, err)
}

// Test_mappingPaths_ListSyntax locks the parser fix: mapping_file accepts the
// same separators as its validation (newline, literal \n, pipe).
func Test_mappingPaths_ListSyntax(t *testing.T) {
	configs := testConfigs("", "a.txt\nb.txt|c.txt")
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, configs.mappingPaths())
}
