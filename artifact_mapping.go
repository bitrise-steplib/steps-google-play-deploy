package main

import (
	"fmt"
	"path/filepath"

	"github.com/bitrise-io/go-android/v2/gradle/artifactmap"
	"github.com/bitrise-io/go-utils/pathutil"
	"github.com/bitrise-io/go-utils/v2/log"
)

// mappingPairing decides which mapping file to upload for each app artifact.
//
// Two sources exist, in order of precedence:
//  1. the variant-keyed artifact map exported by the Android build steps —
//     it pairs an app file with its own variant's mapping by identity, and
//     wins whenever it knows the artifact;
//  2. the positional mapping_file input — entry N belongs to app N. Kept for
//     backwards compatibility and used for apps the artifact map doesn't know.
type mappingPairing struct {
	mapPath    string
	byAppPath  map[string]variantMapping
	positional []string
}

// variantMapping is what the artifact map knows about one app file.
type variantMapping struct {
	variant string
	// mappingPath is empty when the variant produced no mapping file.
	mappingPath string
}

// newMappingPairing loads the artifact map named by the config (when it is set
// and the file exists) and combines it with the positional mapping_file input.
func newMappingPairing(configs Configs, logger log.Logger) (mappingPairing, error) {
	pairing := mappingPairing{positional: configs.mappingPaths()}

	if configs.ArtifactMapPath == "" {
		return pairing, nil
	}
	if exists, err := pathutil.IsPathExists(configs.ArtifactMapPath); err != nil {
		return mappingPairing{}, fmt.Errorf("failed to check if artifact map exists at: %s, error: %s", configs.ArtifactMapPath, err)
	} else if !exists {
		logger.Debugf("No artifact map at %s, using the mapping_file input only", configs.ArtifactMapPath)
		return pairing, nil
	}

	m, err := artifactmap.Read(configs.ArtifactMapPath)
	if err != nil {
		return mappingPairing{}, err
	}

	pairing.mapPath = configs.ArtifactMapPath
	pairing.byAppPath = map[string]variantMapping{}
	for _, key := range m.SortedVariantKeys() {
		entry := m.Variants[key]
		vm := variantMapping{
			variant:     key,
			mappingPath: artifactmap.Resolve(configs.ArtifactMapPath, entry.Mapping),
		}
		for _, name := range append(append([]string{}, entry.APK...), entry.AAB...) {
			pairing.byAppPath[filepath.Clean(artifactmap.Resolve(configs.ArtifactMapPath, name))] = vm
		}
	}
	return pairing, nil
}

// usesArtifactMap reports whether an artifact map was loaded.
func (p mappingPairing) usesArtifactMap() bool {
	return p.mapPath != ""
}

// mappingFor returns the mapping file to upload for the app at appIndex
// (appPath), or "" when there is nothing to upload. It logs which source made
// the decision, and loudly flags when the artifact map overrides a
// mapping_file entry the user also provided.
func (p mappingPairing) mappingFor(appIndex int, appPath string, logger log.Logger) string {
	positional := ""
	if appIndex < len(p.positional) {
		positional = p.positional[appIndex]
	}

	if !p.usesArtifactMap() {
		return positional
	}

	vm, known := p.byAppPath[filepath.Clean(appPath)]
	if !known {
		logger.Warnf("The artifact map (%s) does not know %s.", p.mapPath, appPath)
		if positional != "" {
			logger.Warnf("Falling back to the mapping_file input for it: %s", positional)
		}
		return positional
	}

	if vm.mappingPath == "" {
		logger.Printf("Artifact map: variant %s has no mapping file, not uploading one for %s", vm.variant, filepath.Base(appPath))
		if positional != "" {
			logger.Warnf("Ignoring the mapping_file input entry (%s) for this artifact: the artifact map is authoritative for artifacts it knows.", positional)
		}
		return ""
	}

	logger.Printf("Artifact map: uploading the mapping file of variant %s: %s", vm.variant, vm.mappingPath)
	if positional != "" && filepath.Clean(positional) != filepath.Clean(vm.mappingPath) {
		logger.Warnf("The mapping_file input names a different file (%s) for this artifact; using the artifact map's pairing instead.", positional)
	}
	return vm.mappingPath
}
