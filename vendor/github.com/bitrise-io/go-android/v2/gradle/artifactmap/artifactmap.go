// Package artifactmap defines the module/variant-keyed artifact map that the
// Android build steps export and the Google Play Deploy step consumes: which
// exported APK/AAB and which mapping.txt belong to the same build variant —
// something the flat BITRISE_*_PATH outputs cannot express, while Google Play
// accepts exactly one mapping per version code.
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
	"strings"
)

// Version is the schema version this package reads and writes.
const Version = 1

// DefaultFileName is the conventional name of the map file inside the deploy
// directory. A producer finding an existing map at this name Merges its own
// entries into it (several build steps in one workflow accumulate a single
// document); an unreadable existing file is replaced. Consumers should use
// the exported path rather than assuming the name.
const DefaultFileName = "android-artifact-map.json"

// EnvKey is the environment variable producers export the map file's path in
// and consumers read it from by default.
const EnvKey = "BITRISE_ANDROID_ARTIFACT_MAP_PATH"

// Map is the top-level document: one build's artifacts, grouped by module and
// variant.
type Map struct {
	Version int `json:"version"`
	// Modules is keyed by the Gradle module ("app"), then by the merged
	// variant name ("demoRelease") — build variants are a per-module concept
	// in Gradle, and two modules can declare identical variant names. The
	// module key is "" when it could not be derived from the build-output
	// path. Consumers pair artifacts by file identity, not by key.
	Modules map[string]map[string]Entry `json:"modules"`
	// Unmatched lists exported files whose module/variant could not be
	// derived from their build-output path, so no export goes silently
	// unaccounted for.
	Unmatched Unmatched `json:"unmatched"`
}

// Entry groups one variant's exported files, as names relative to the map
// file's directory.
type Entry struct {
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
// copied from: the map references DeployPath's base name, while the module and
// variant are derived from SourcePath (the deploy dir is flat; only the Gradle
// output tree encodes them).
type File struct {
	DeployPath string
	SourcePath string
}

// Label renders a module/variant pair for logs: "app/demoRelease", or the
// bare variant name when the module is unknown.
func Label(module, variant string) string {
	if module == "" {
		return variant
	}
	return module + "/" + variant
}

// mappingRank orders competing mapping files of one variant: a file literally
// named mapping.txt (the shrinker's real output) outranks sibling report
// files (usage.txt, seeds.txt, ...) matched by a widened filter, and within
// equals, the official build/outputs/ copy outranks the intermediates/
// task-workdir copy AGP writes first. Ties keep the later file.
func mappingRank(f File) int {
	rank := 0
	if filepath.Base(f.SourcePath) == "mapping.txt" {
		rank += 2
	}
	if strings.Contains(filepath.ToSlash(f.SourcePath), "/outputs/") {
		rank++
	}
	return rank
}

// Build assembles a Map from the files a step exported. Modules and variants
// are derived from each file's SourcePath; unrecognisable paths land under
// Unmatched. When several mapping files resolve to the same variant, the
// highest-ranked one wins (see mappingRank); every dropped file is reported
// in warnings.
func Build(apks, aabs, mappings []File) (Map, []string) {
	type group struct {
		variant     ArtifactVariant
		entry       Entry
		mappingRank int
	}
	groups := map[ArtifactVariant]*group{}
	var warnings []string

	grab := func(f File) (*group, bool) {
		variant, ok := VariantFromPath(f.SourcePath)
		if !ok {
			return nil, false
		}
		g, ok := groups[variant]
		if !ok {
			g = &group{variant: variant, entry: Entry{AAB: []string{}, APK: []string{}}}
			groups[variant] = g
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
		rank := mappingRank(f)
		label := Label(g.variant.Module, g.variant.Variant)
		switch {
		case g.entry.Mapping == "" || g.entry.Mapping == name:
			// first mapping for the variant (or a re-listing of the same file)
		case rank < g.mappingRank:
			// never displace a higher-ranked mapping (the real mapping.txt,
			// or the official outputs/ copy) with a lower-ranked one
			warnings = append(warnings, fmt.Sprintf(
				"variant %s matched several mapping files: keeping %s, dropping %s",
				label, g.entry.Mapping, name))
			continue
		default:
			warnings = append(warnings, fmt.Sprintf(
				"variant %s matched several mapping files: keeping %s, dropping %s",
				label, name, g.entry.Mapping))
		}
		g.entry.Mapping = name
		g.mappingRank = rank
	}

	modules := map[string]map[string]Entry{}
	for variant, g := range groups {
		// producers discover files in filesystem-walk order, which is not a
		// meaningful contract; sort so the document is deterministic
		sort.Strings(g.entry.APK)
		sort.Strings(g.entry.AAB)
		setEntry(modules, variant.Module, variant.Variant, g.entry)
	}
	sort.Strings(unmatched.APK)
	sort.Strings(unmatched.AAB)
	sort.Strings(unmatched.Mapping)

	return Map{Version: Version, Modules: modules, Unmatched: unmatched}, warnings
}

func setEntry(modules map[string]map[string]Entry, module, variant string, entry Entry) {
	if modules[module] == nil {
		modules[module] = map[string]Entry{}
	}
	modules[module][variant] = entry
}

// Merge combines the map written by an earlier step run (base) with the map of
// the current run (overlay), so several producer steps in one workflow
// accumulate a single document instead of the last one overwriting the rest.
// Entries are matched by module and variant and merged field-wise: a field the
// overlay produced replaces the base's (a rebuild — reported in warnings when
// the values differ), fields the overlay didn't produce keep the base's. An
// apk-building run and an aab-building run of the same variant therefore
// accumulate one complete entry. Unmatched lists are unioned and deduped.
func Merge(base, overlay Map) (Map, []string) {
	var warnings []string

	modules := map[string]map[string]Entry{}
	for _, module := range base.SortedModules() {
		for _, variant := range base.SortedVariants(module) {
			setEntry(modules, module, variant, base.Modules[module][variant])
		}
	}
	for _, module := range overlay.SortedModules() {
		for _, variant := range overlay.SortedVariants(module) {
			entry := overlay.Modules[module][variant]
			if baseEntry, exists := modules[module][variant]; exists {
				merged, mergeWarnings := mergeEntries(baseEntry, entry, Label(module, variant))
				warnings = append(warnings, mergeWarnings...)
				entry = merged
			}
			setEntry(modules, module, variant, entry)
		}
	}

	unmatched := Unmatched{
		APK:     unionSorted(base.Unmatched.APK, overlay.Unmatched.APK),
		AAB:     unionSorted(base.Unmatched.AAB, overlay.Unmatched.AAB),
		Mapping: unionSorted(base.Unmatched.Mapping, overlay.Unmatched.Mapping),
	}

	return Map{Version: Version, Modules: modules, Unmatched: unmatched}, warnings
}

// mergeEntries combines one variant's base and overlay entries field-wise:
// what the overlay produced wins, what it didn't produce survives from the
// base. Replacing a differing earlier value is reported.
func mergeEntries(base, overlay Entry, label string) (Entry, []string) {
	var warnings []string
	merged := overlay

	if len(overlay.APK) == 0 {
		merged.APK = base.APK
	} else if len(base.APK) != 0 && !stringSlicesEqual(base.APK, overlay.APK) {
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its APKs, the artifact map now references the newer files", label))
	}

	if len(overlay.AAB) == 0 {
		merged.AAB = base.AAB
	} else if len(base.AAB) != 0 && !stringSlicesEqual(base.AAB, overlay.AAB) {
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its AABs, the artifact map now references the newer files", label))
	}

	if overlay.Mapping == "" {
		merged.Mapping = base.Mapping
	} else if base.Mapping != "" && base.Mapping != overlay.Mapping {
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its mapping file, the artifact map now references %s instead of %s",
			label, overlay.Mapping, base.Mapping))
	}

	return merged, warnings
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func unionSorted(a, b []string) []string {
	seen := map[string]bool{}
	union := []string{}
	for _, list := range [][]string{a, b} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				union = append(union, name)
			}
		}
	}
	sort.Strings(union)
	return union
}

// ReplaceFile renames a file reference wherever it appears — the variants'
// APK/AAB lists and the unmatched lists — and reports whether anything was
// replaced. A step that transforms an artifact in the deploy dir under a new
// name (e.g. signing) uses it to keep the map pointing at the current file.
// Mapping references are left alone: artifact transforms don't touch them.
func (m *Map) ReplaceFile(oldName, newName string) bool {
	replaced := false
	replaceIn := func(list []string) {
		changed := false
		for i, name := range list {
			if name == oldName {
				list[i] = newName
				changed = true
			}
		}
		if changed {
			sort.Strings(list)
			replaced = true
		}
	}
	for _, variants := range m.Modules {
		for _, entry := range variants {
			replaceIn(entry.APK)
			replaceIn(entry.AAB)
		}
	}
	replaceIn(m.Unmatched.APK)
	replaceIn(m.Unmatched.AAB)
	return replaced
}

// IsEmpty reports whether the map carries no artifacts at all — producers can
// skip writing a file for such a build.
func (m Map) IsEmpty() bool {
	for _, variants := range m.Modules {
		if len(variants) > 0 {
			return false
		}
	}
	return len(m.Unmatched.APK) == 0 && len(m.Unmatched.AAB) == 0 && len(m.Unmatched.Mapping) == 0
}

// SortedModules returns the module keys in lexical order, for deterministic
// logging and iteration.
func (m Map) SortedModules() []string {
	modules := make([]string, 0, len(m.Modules))
	for module := range m.Modules {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	return modules
}

// SortedVariants returns a module's variant keys in lexical order, for
// deterministic logging and iteration.
func (m Map) SortedVariants(module string) []string {
	variants := make([]string, 0, len(m.Modules[module]))
	for variant := range m.Modules[module] {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	return variants
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

// Marshal renders the document exactly as Write persists it — indented, with
// a trailing newline — so steps can also print it to the build log.
func Marshal(m Map) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal artifact map: %w", err)
	}
	return append(data, '\n'), nil
}

// Write marshals the map and writes it to path. The document is indented so
// the exported build artifact stays human-readable.
func Write(path string, m Map) error {
	data, err := Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
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
