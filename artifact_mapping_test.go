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
  "modules": {
    "app": {
      "demoRelease": {
        "mapping": "mapping.txt",
        "aab": ["app-demo-release.aab"],
        "apk": ["app-demo-release.apk"]
      },
      "paidRelease": {
        "mapping": "mapping-20260805121530.txt",
        "aab": ["app-paid-release.aab"],
        "apk": []
      },
      "freeDebug": {
        "aab": [],
        "apk": ["app-free-debug.apk"]
      }
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
	pairing := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-paid-release.aab"))

	assert.Equal(t, filepath.Join(deployDir, "mapping-20260805121530.txt"), got)
}

func Test_mappingFor_MapWinsOverPositionalInput(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, "/somewhere/else/mapping.txt"), log.NewLogger())

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-demo-release.aab"))

	assert.Equal(t, filepath.Join(deployDir, "mapping.txt"), got)
}

func Test_mappingFor_KnownVariantWithoutMapping_FallsBackToPositional(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, "/somewhere/else/mapping.txt"), log.NewLogger())

	// the map knows app-free-debug.apk but records no mapping for freeDebug:
	// the user's explicit input must not be suppressed on such weak evidence
	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-free-debug.apk"))

	assert.Equal(t, "/somewhere/else/mapping.txt", got)
}

func Test_mappingFor_KnownVariantWithoutMapping_NoPositional_UploadsNothing(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-free-debug.apk"))

	assert.Equal(t, "", got)
}

func Test_mappingFor_UnknownApp_FallsBackToPositional(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, "first-mapping.txt|second-mapping.txt"), log.NewLogger())

	got := pairing.mappingFor(1, filepath.Join(deployDir, "hand-built.aab"))

	assert.Equal(t, "second-mapping.txt", got)
}

func Test_mappingFor_NoMapConfigured_KeepsPositionalBehavior(t *testing.T) {
	pairing := newMappingPairing(testConfigs("", "a.txt|b.txt"), log.NewLogger())

	assert.False(t, pairing.usesArtifactMap())
	assert.Equal(t, "a.txt", pairing.mappingFor(0, "/deploy/app1.aab"))
	assert.Equal(t, "b.txt", pairing.mappingFor(1, "/deploy/app2.aab"))
	assert.Equal(t, "", pairing.mappingFor(2, "/deploy/app3.aab"))
}

func Test_mappingFor_MissingMapFile_KeepsPositionalBehavior(t *testing.T) {
	pairing := newMappingPairing(testConfigs("/nonexistent/android-artifact-map.json", "a.txt"), log.NewLogger())

	assert.False(t, pairing.usesArtifactMap())
	assert.Equal(t, "a.txt", pairing.mappingFor(0, "/deploy/app1.aab"))
}

// Test_newMappingPairing_UnreadableMapIsIgnored: the map only ever upgrades
// pairing — garbage or a newer schema version must not fail the deploy, only
// disable the map.
func Test_newMappingPairing_UnreadableMapIsIgnored(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":        `{"not": "an artifact map"}`,
		"newer schema":   `{"version": 99, "variants": {}}`,
		"malformed json": `{{{`,
	} {
		t.Run(name, func(t *testing.T) {
			mapPath := filepath.Join(t.TempDir(), "android-artifact-map.json")
			require.NoError(t, os.WriteFile(mapPath, []byte(content), 0600))

			pairing := newMappingPairing(testConfigs(mapPath, "a.txt"), log.NewLogger())

			assert.False(t, pairing.usesArtifactMap())
			assert.Equal(t, "a.txt", pairing.mappingFor(0, "/deploy/app1.aab"), "must fall back to positional pairing")
		})
	}
}

// Test_mappingFor_SignedArtifact_MatchesUnsignedOrigin: sign-apk renames
// artifacts to *-bitrise-signed.* next to the original; until sign-apk updates
// the map itself, the consumer matches the signed file back to its origin.
func Test_mappingFor_SignedArtifact_MatchesUnsignedOrigin(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())

	got := pairing.mappingFor(0, filepath.Join(deployDir, "app-demo-release-bitrise-signed.aab"))

	assert.Equal(t, filepath.Join(deployDir, "mapping.txt"), got)
}

func Test_mappingFor_SignedArtifactUnknownOrigin_FallsBackToPositional(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	pairing := newMappingPairing(testConfigs(mapPath, "a.txt"), log.NewLogger())

	got := pairing.mappingFor(0, filepath.Join(deployDir, "custom-name-bitrise-signed.aab"))

	assert.Equal(t, "a.txt", got)
}

// Test_mappingFor_SymlinkedAppPath_StillMatches: on macOS stacks the deploy
// dir is often reachable through a symlink (/tmp vs /private/tmp); the lookup
// must compare canonical identities, not spellings.
func Test_mappingFor_SymlinkedAppPath_StillMatches(t *testing.T) {
	deployDir, mapPath := writeTestArtifactMap(t)
	// the lookup resolves symlinks only for files that exist
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "app-demo-release.aab"), []byte("aab"), 0600))

	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(deployDir, linkDir))

	pairing := newMappingPairing(testConfigs(mapPath, ""), log.NewLogger())
	got := pairing.mappingFor(0, filepath.Join(linkDir, "app-demo-release.aab"))

	assert.Equal(t, filepath.Join(deployDir, "mapping.txt"), got)
}

// Test_mappingPaths_ListSyntax locks the parser fix: mapping_file accepts the
// same separators as its validation (newline, literal \n, pipe).
func Test_mappingPaths_ListSyntax(t *testing.T) {
	configs := testConfigs("", "a.txt\nb.txt|c.txt")
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, configs.mappingPaths())
}
