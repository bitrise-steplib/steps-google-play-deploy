package main

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-android/v2/gradle/artifactmap"
	"github.com/bitrise-io/go-utils/v2/log"
)

// signedArtifactSuffix is the rename the sign-apk step applies to the
// artifacts it signs.
const signedArtifactSuffix = "-bitrise-signed"

// mappingPairing decides which mapping file to upload for each app artifact.
//
// Two sources exist, in order of precedence:
//  1. the variant-keyed artifact map exported by the Android build steps —
//     it pairs an app file with its own variant's mapping by identity, and
//     wins whenever it names a mapping for the artifact;
//  2. the positional mapping_file input — entry N belongs to app N. Kept for
//     backwards compatibility, and used whenever the map has nothing to say
//     (no map, unknown artifact, variant without a mapping).
type mappingPairing struct {
	logger     log.Logger
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

// newMappingPairing loads the artifact map named by the config and combines it
// with the positional mapping_file input. The map only ever upgrades pairing —
// a missing, unreadable or newer-versioned map is reported and ignored, never
// failing the deploy over auxiliary deobfuscation data.
func newMappingPairing(configs Configs, logger log.Logger) mappingPairing {
	pairing := mappingPairing{logger: logger, positional: configs.mappingPaths()}
	if configs.ArtifactMapPath == "" {
		return pairing
	}

	m, err := artifactmap.Read(configs.ArtifactMapPath)
	if errors.Is(err, fs.ErrNotExist) {
		logger.Debugf("No artifact map at %s, using the mapping_file input only", configs.ArtifactMapPath)
		return pairing
	}
	if err != nil {
		logger.Warnf("Ignoring the artifact map at %s: %s", configs.ArtifactMapPath, err)
		logger.Warnf("Mapping files are paired positionally from the mapping_file input.")
		return pairing
	}

	logger.Infof("Using artifact map from: %s", configs.ArtifactMapPath)
	pairing.mapPath = configs.ArtifactMapPath
	pairing.byAppPath = map[string]variantMapping{}
	for _, key := range m.SortedVariantKeys() {
		entry := m.Variants[key]
		vm := variantMapping{
			variant:     key,
			mappingPath: artifactmap.Resolve(configs.ArtifactMapPath, entry.Mapping),
		}
		for _, list := range [][]string{entry.APK, entry.AAB} {
			for _, name := range list {
				pairing.byAppPath[canonicalPath(artifactmap.Resolve(configs.ArtifactMapPath, name))] = vm
			}
		}
		mappingInfo := entry.Mapping
		if mappingInfo == "" {
			mappingInfo = "none"
		}
		logger.Printf("- %s: %d APK, %d AAB, mapping: %s", key, len(entry.APK), len(entry.AAB), mappingInfo)
	}
	// The full document for post-hoc debugging, without re-downloading the artifact.
	if doc, err := artifactmap.Marshal(m); err == nil {
		logger.Debugf("Artifact map contents:\n%s", strings.TrimSuffix(string(doc), "\n"))
	}
	return pairing
}

// usesArtifactMap reports whether an artifact map was loaded.
func (p mappingPairing) usesArtifactMap() bool {
	return p.byAppPath != nil
}

// mappingFor returns the mapping file to upload for the app at appIndex
// (appPath), or "" when there is nothing to upload. It logs which source made
// the decision, and loudly flags when the artifact map overrides a
// mapping_file entry the user also provided.
func (p mappingPairing) mappingFor(appIndex int, appPath string) string {
	positional := ""
	if appIndex < len(p.positional) {
		positional = p.positional[appIndex]
	}

	if !p.usesArtifactMap() {
		return positional
	}

	vm, known := p.lookup(appPath)
	if !known {
		p.logger.Warnf("The artifact map (%s) does not know %s.", p.mapPath, appPath)
		if positional != "" {
			p.logger.Warnf("Falling back to the mapping_file input for it: %s", positional)
		}
		return positional
	}

	if vm.mappingPath == "" {
		p.logger.Printf("Artifact map: variant %s has no mapping file for %s", vm.variant, filepath.Base(appPath))
		if positional != "" {
			p.logger.Warnf("Falling back to the mapping_file input for it: %s", positional)
		}
		return positional
	}

	p.logger.Printf("Artifact map: uploading the mapping file of variant %s: %s", vm.variant, vm.mappingPath)
	if positional != "" && canonicalPath(positional) != canonicalPath(vm.mappingPath) {
		p.logger.Warnf("The mapping_file input names a different file (%s) for this artifact; using the artifact map's pairing instead.", positional)
	}
	return vm.mappingPath
}

// lookup finds what the artifact map knows about an app file. Exact identity
// first; a *-bitrise-signed.* file (the sign-apk step's rename, written next
// to the original) is matched back to its unsigned origin until sign-apk
// versions that update the map themselves are widespread.
func (p mappingPairing) lookup(appPath string) (variantMapping, bool) {
	if vm, ok := p.byAppPath[canonicalPath(appPath)]; ok {
		return vm, true
	}

	base := filepath.Base(appPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, signedArtifactSuffix) {
		original := filepath.Join(filepath.Dir(appPath), strings.TrimSuffix(stem, signedArtifactSuffix)+ext)
		if vm, ok := p.byAppPath[canonicalPath(original)]; ok {
			p.logger.Printf("Artifact map: matched %s to variant %s via its unsigned origin %s", base, vm.variant, filepath.Base(original))
			return vm, true
		}
	}

	return variantMapping{}, false
}

// canonicalPath normalizes a path to a comparable identity: absolute, symlinks
// resolved, cleaned. Symlink resolution matters on macOS stacks where the
// deploy dir is reachable both as /tmp/... and /private/tmp/...; resolution
// failures (e.g. the file does not exist) degrade to the cleaned absolute
// form.
func canonicalPath(pth string) string {
	abs, err := filepath.Abs(pth)
	if err != nil {
		return filepath.Clean(pth)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}
