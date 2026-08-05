// Package artifactmap defines the variant-keyed artifact map that the Android
// build steps export and the Google Play Deploy step consumes: which exported
// APK/AAB and which mapping.txt belong to the same build variant — something
// the flat BITRISE_*_PATH outputs cannot express, while Google Play accepts
// exactly one mapping per version code.
//
// Producers write the map as JSON into the deploy directory and export its
// path as BITRISE_ANDROID_ARTIFACT_MAP_PATH. File references are bare names
// relative to the map file's directory (see Resolve), so the map survives the
// directory being archived or moved. The package is stdlib-only so consumers
// can vendor it alone.
//
// # Schema evolution
//
// Producers and consumers are version-pinned independently in user workflows.
// Additive fields keep the version number (unknown JSON fields are ignored);
// the version bumps only on breaking layout changes, and Read rejects
// documents newer than it understands.
package artifactmap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Version is the schema version this package reads and writes.
const Version = 1

// DefaultFileName is the conventional name of the map file inside the deploy
// directory. Producers overwrite any existing file at this name (the map is
// regenerated authoritative metadata); consumers should use the exported path
// rather than assuming the name.
const DefaultFileName = "android-artifact-map.json"

// EnvKey is the environment variable producers export the map file's path in
// and consumers read it from by default.
const EnvKey = "BITRISE_ANDROID_ARTIFACT_MAP_PATH"

// Map is the top-level document: one build's artifacts, grouped by variant.
type Map struct {
	Version int `json:"version"`
	// Variants is keyed by the merged Gradle variant name ("demoRelease");
	// colliding names across modules are disambiguated as "module/variant".
	// The key is informational — consumers pair artifacts by file identity.
	Variants map[string]Entry `json:"variants"`
	// Unmatched lists exported files whose variant could not be derived from
	// their build-output path, so no export goes silently unaccounted for.
	Unmatched Unmatched `json:"unmatched"`
}

// Entry groups one variant's exported files, as names relative to the map
// file's directory.
type Entry struct {
	// Module is the Gradle module ("app"); empty when it could not be derived.
	Module string `json:"module"`
	// Mapping is the variant's R8/ProGuard mapping file; empty when none.
	Mapping string `json:"mapping,omitempty"`
	// AAB lists the variant's app bundles (in practice at most one).
	AAB []string `json:"aab"`
	// APK lists the variant's APKs (several with ABI/density splits).
	APK []string `json:"apk"`
}

// Unmatched lists exported files that could not be attributed to a variant.
type Unmatched struct {
	APK     []string `json:"apk"`
	AAB     []string `json:"aab"`
	Mapping []string `json:"mapping"`
}

// File pairs a file's deploy-dir location with the build-output path it was
// copied from: the map references DeployPath's base name, while the variant is
// derived from SourcePath (the deploy dir is flat; only the Gradle output tree
// encodes the variant).
type File struct {
	DeployPath string
	SourcePath string
}

// Build assembles a Map from the files a step exported. Variants are derived
// from each file's SourcePath; unrecognisable paths land under Unmatched.
// When several mapping files resolve to the same variant, a file literally
// named mapping.txt wins, otherwise the last one; every dropped file is
// reported in warnings.
func Build(apks, aabs, mappings []File) (Map, []string) {
	type group struct {
		variant ArtifactVariant
		entry   Entry
		// the current mapping is a file literally named mapping.txt (the
		// shrinker's real output) and must not be displaced by sibling
		// report files (usage.txt, seeds.txt, ...) matched by a wide filter
		mappingIsCanonical bool
	}
	groups := map[ArtifactVariant]*group{}
	ordered := []*group{} // deterministic iteration for key assignment
	var warnings []string

	grab := func(f File) (*group, bool) {
		variant, ok := VariantFromPath(f.SourcePath)
		if !ok {
			return nil, false
		}
		g, ok := groups[variant]
		if !ok {
			g = &group{variant: variant, entry: Entry{Module: variant.Module, AAB: []string{}, APK: []string{}}}
			groups[variant] = g
			ordered = append(ordered, g)
		}
		return g, true
	}

	unmatched := Unmatched{APK: []string{}, AAB: []string{}, Mapping: []string{}}
	for _, f := range apks {
		if g, ok := grab(f); ok {
			g.entry.APK = append(g.entry.APK, filepath.Base(f.DeployPath))
		} else {
			unmatched.APK = append(unmatched.APK, filepath.Base(f.DeployPath))
		}
	}
	for _, f := range aabs {
		if g, ok := grab(f); ok {
			g.entry.AAB = append(g.entry.AAB, filepath.Base(f.DeployPath))
		} else {
			unmatched.AAB = append(unmatched.AAB, filepath.Base(f.DeployPath))
		}
	}
	for _, f := range mappings {
		g, ok := grab(f)
		if !ok {
			unmatched.Mapping = append(unmatched.Mapping, filepath.Base(f.DeployPath))
			continue
		}
		name := filepath.Base(f.DeployPath)
		canonical := filepath.Base(f.SourcePath) == "mapping.txt"
		switch {
		case g.entry.Mapping == "" || g.entry.Mapping == name:
			// first mapping for the variant (or a re-listing of the same file)
		case g.mappingIsCanonical && !canonical:
			// never displace the shrinker's real mapping.txt with a sibling
			// report file that a widened filter also matched
			warnings = append(warnings, fmt.Sprintf(
				"variant %s matched several mapping files: keeping %s (named mapping.txt), dropping %s",
				g.variant.Variant, g.entry.Mapping, name))
			continue
		default:
			warnings = append(warnings, fmt.Sprintf(
				"variant %s matched several mapping files: keeping %s, dropping %s",
				g.variant.Variant, name, g.entry.Mapping))
		}
		g.entry.Mapping = name
		g.mappingIsCanonical = canonical
	}

	// Key by variant name; disambiguate as module/variant only on collision.
	nameCount := map[string]int{}
	for _, g := range ordered {
		nameCount[g.variant.Variant]++
	}
	variants := map[string]Entry{}
	for _, g := range ordered {
		key := g.variant.Variant
		if nameCount[g.variant.Variant] > 1 {
			key = g.variant.Module + "/" + g.variant.Variant
		}
		// The producers discover files in filesystem-walk order, which is not a
		// meaningful contract; sort so the document is deterministic.
		sort.Strings(g.entry.APK)
		sort.Strings(g.entry.AAB)
		variants[key] = g.entry
	}
	sort.Strings(unmatched.APK)
	sort.Strings(unmatched.AAB)
	sort.Strings(unmatched.Mapping)

	return Map{Version: Version, Variants: variants, Unmatched: unmatched}, warnings
}

// IsEmpty reports whether the map carries no artifacts at all — producers can
// skip writing a file for such a build.
func (m Map) IsEmpty() bool {
	return len(m.Variants) == 0 &&
		len(m.Unmatched.APK) == 0 && len(m.Unmatched.AAB) == 0 && len(m.Unmatched.Mapping) == 0
}

// SortedVariantKeys returns the variant keys in lexical order, for
// deterministic logging and iteration.
func (m Map) SortedVariantKeys() []string {
	keys := make([]string, 0, len(m.Variants))
	for key := range m.Variants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Resolve turns a file name referenced by the map (an Entry/Unmatched value)
// into a full path against the map file's own directory. An empty name
// resolves to "".
func Resolve(mapPath, name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(mapPath), name)
}

// Write marshals the map and writes it to path. The document is indented so
// the exported build artifact stays human-readable.
func Write(path string, m Map) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact map: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write artifact map: %w", err)
	}
	return nil
}

// Read loads and validates a map written by Write. It rejects documents with a
// newer schema version than this package understands, and documents that are
// not artifact maps at all (missing version).
func Read(path string) (Map, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Map{}, fmt.Errorf("read artifact map: %w", err)
	}
	var m Map
	if err := json.Unmarshal(data, &m); err != nil {
		return Map{}, fmt.Errorf("parse artifact map %s: %w", path, err)
	}
	if m.Version == 0 {
		return Map{}, fmt.Errorf("parse artifact map %s: missing schema version, not an artifact map", path)
	}
	if m.Version > Version {
		return Map{}, fmt.Errorf("artifact map %s has schema version %d, this consumer understands up to %d", path, m.Version, Version)
	}
	return m, nil
}
