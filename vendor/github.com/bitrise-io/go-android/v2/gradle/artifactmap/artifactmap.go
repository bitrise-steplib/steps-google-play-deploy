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
// documents newer than it understands (ErrNewerVersion — a merging producer
// must then leave the file untouched). Merge and Write re-encode the document
// with this package's schema, so additive fields an older step doesn't know
// are dropped from the merged document: additive fields must stay optional
// for consumers.
package artifactmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Version is the schema version this package reads and writes.
const Version = 1

// DefaultFileName is the conventional name of the map file inside the deploy
// directory. A producer finding an existing map at this name Merges into it;
// an unreadable existing file is replaced. Consumers should use the exported
// path rather than assuming the name.
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
	// ModuleAARs lists library archives per module, keyed like Modules. AGP's
	// aar layout (outputs/aar/<name>.aar) encodes no variant, so AARs attach
	// to their module — the path before /build/ — instead of a variant entry.
	ModuleAARs map[string][]string `json:"module_aars,omitempty"`
	// Unmatched lists exported files the map cannot attribute: unrecognised
	// locations, and report files a widened mapping filter dragged in. Files
	// from Gradle's build/intermediates/ tree are left out of the document
	// entirely (see Build).
	Unmatched Unmatched `json:"unmatched"`
	// Sources records, for every file name the document references, the
	// build-output path it was copied from — collision-renamed deploy names
	// explain themselves without the build log. A debugging aid only:
	// best-effort across merges (an older step re-writes the document without
	// it), so consumers must not rely on it.
	Sources map[string]string `json:"sources,omitempty"`
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
	AAR     []string `json:"aar"`
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

// canonicalMapping reports whether the file is the shrinker's real output — a
// file literally named mapping.txt. Nothing else (usage.txt, seeds.txt and
// other report files matched by a widened filter) can be a variant's mapping.
func canonicalMapping(f File) bool {
	return filepath.Base(f.SourcePath) == "mapping.txt"
}

// fromIntermediates reports whether the file comes from Gradle's
// build/intermediates/ tree — task-workdir duplicates of the official
// outputs. Build leaves them out of the map entirely. Anchored on the
// build/ parent so a checkout or module directory that happens to be named
// "intermediates" is not swallowed.
func fromIntermediates(f File) bool {
	return strings.Contains(filepath.ToSlash(f.SourcePath), "/build/intermediates/")
}

// aarModulePath returns the module path of an official build/outputs/aar file.
// AGP's aar layout encodes no variant, so unlike VariantFromPath only the
// module is derived; the same right-to-left marker scan keeps a checkout
// directory named "outputs" from hijacking parsing.
func aarModulePath(path string) (modulePath string, ok bool) {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i := len(segments) - 2; i >= 0; i-- {
		if segments[i] != "outputs" || segments[i+1] != "aar" {
			continue
		}
		_, modulePath := moduleFromSegments(segments[:i])
		return modulePath, true
	}
	return "", false
}

// Build assembles a Map from the files a step exported. Modules and variants
// are derived from each file's SourcePath. Only official build/outputs/ paths
// pair, and only a file literally named mapping.txt can be a variant's
// mapping; other exports stay visible under Unmatched — except files from
// build/intermediates/ (task-workdir duplicates of the outputs), which are
// left out of the document entirely.
func Build(apks, aabs, aars, mappings []File) (Map, []string) {
	type group struct {
		variant ArtifactVariant
		entry   Entry
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

	sources := map[string]string{}
	unmatched := Unmatched{APK: []string{}, AAB: []string{}, AAR: []string{}, Mapping: []string{}}
	for _, f := range apks {
		if fromIntermediates(f) {
			continue
		}
		sources[filepath.Base(f.DeployPath)] = f.SourcePath
		if g, ok := grab(f); ok {
			g.entry.APK = append(g.entry.APK, filepath.Base(f.DeployPath))
		} else {
			unmatched.APK = append(unmatched.APK, filepath.Base(f.DeployPath))
		}
	}
	for _, f := range aabs {
		if fromIntermediates(f) {
			continue
		}
		sources[filepath.Base(f.DeployPath)] = f.SourcePath
		if g, ok := grab(f); ok {
			g.entry.AAB = append(g.entry.AAB, filepath.Base(f.DeployPath))
		} else {
			unmatched.AAB = append(unmatched.AAB, filepath.Base(f.DeployPath))
		}
	}
	moduleAARs := map[string][]string{}
	for _, f := range aars {
		if fromIntermediates(f) {
			continue
		}
		sources[filepath.Base(f.DeployPath)] = f.SourcePath
		if modulePath, ok := aarModulePath(f.SourcePath); ok {
			moduleAARs[modulePath] = append(moduleAARs[modulePath], filepath.Base(f.DeployPath))
		} else {
			unmatched.AAR = append(unmatched.AAR, filepath.Base(f.DeployPath))
		}
	}
	for _, f := range mappings {
		if fromIntermediates(f) {
			continue
		}
		name := filepath.Base(f.DeployPath)
		sources[name] = f.SourcePath
		if !canonicalMapping(f) {
			unmatched.Mapping = append(unmatched.Mapping, name)
			continue
		}
		g, ok := grab(f)
		if !ok {
			unmatched.Mapping = append(unmatched.Mapping, name)
			continue
		}
		if g.entry.Mapping != "" && g.entry.Mapping != name {
			warnings = append(warnings, fmt.Sprintf(
				"variant %s matched several mapping files: keeping %s, dropping %s",
				Label(g.variant.Module, g.variant.Variant), name, g.entry.Mapping))
		}
		g.entry.Mapping = name
	}

	var modulePaths []string
	seenPath := map[string]bool{}
	notePath := func(path string) {
		if !seenPath[path] {
			seenPath[path] = true
			modulePaths = append(modulePaths, path)
		}
	}
	for variant := range groups {
		notePath(variant.ModulePath)
	}
	for path := range moduleAARs {
		notePath(path)
	}
	keyForPath := moduleKeys(modulePaths)

	modules := map[string]map[string]Entry{}
	for variant, g := range groups {
		// A mapping with no app artifact usually means the APK/AAB resolved to
		// a different variant name — e.g. unexpected nesting under outputs/
		// turned "demoRelease" into a phantom "demoReleaseExtra" key — leaving
		// the pair unpairable with no unmatched entry to show for it.
		if g.entry.Mapping != "" && len(g.entry.APK) == 0 && len(g.entry.AAB) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"variant %s has a mapping but no app artifact: if an APK/AAB was exported for it, its output path may use unexpected nesting",
				Label(variant.Module, variant.Variant)))
		}
		// filesystem-walk order is not a contract; sort for determinism
		sort.Strings(g.entry.APK)
		sort.Strings(g.entry.AAB)
		setEntry(modules, keyForPath[variant.ModulePath], variant.Variant, g.entry)
	}
	var keyedAARs map[string][]string
	if len(moduleAARs) > 0 {
		keyedAARs = map[string][]string{}
		for path, names := range moduleAARs {
			sort.Strings(names)
			keyedAARs[keyForPath[path]] = names
		}
	}
	sort.Strings(unmatched.APK)
	sort.Strings(unmatched.AAB)
	sort.Strings(unmatched.AAR)
	sort.Strings(unmatched.Mapping)

	m := Map{Version: Version, Modules: modules, ModuleAARs: keyedAARs, Unmatched: unmatched, Sources: sources}
	m.pruneSources()
	return m, warnings
}

// referencedNames returns every file name the document references.
func (m Map) referencedNames() map[string]bool {
	names := map[string]bool{}
	for _, variants := range m.Modules {
		for _, entry := range variants {
			if entry.Mapping != "" {
				names[entry.Mapping] = true
			}
			for _, name := range entry.APK {
				names[name] = true
			}
			for _, name := range entry.AAB {
				names[name] = true
			}
		}
	}
	for _, moduleNames := range m.ModuleAARs {
		for _, name := range moduleNames {
			names[name] = true
		}
	}
	for _, name := range m.Unmatched.APK {
		names[name] = true
	}
	for _, name := range m.Unmatched.AAB {
		names[name] = true
	}
	for _, name := range m.Unmatched.AAR {
		names[name] = true
	}
	for _, name := range m.Unmatched.Mapping {
		names[name] = true
	}
	return names
}

// pruneSources drops source records of file names the document no longer
// references (a replaced duplicate mapping, an entry a Merge overwrote).
func (m *Map) pruneSources() {
	names := m.referencedNames()
	for name := range m.Sources {
		if !names[name] {
			delete(m.Sources, name)
		}
	}
	if len(m.Sources) == 0 {
		m.Sources = nil
	}
}

// moduleKeys returns a document key for each distinct module path: the
// directory basename, extended with parent directories until unique among the
// given paths ("app"; "brandA/app" vs "brandB/app" on a basename collision),
// so unrelated modules never merge under one key.
func moduleKeys(paths []string) map[string]string {
	suffix := func(path string, depth int) string {
		segments := strings.Split(path, "/")
		if depth > len(segments) {
			depth = len(segments)
		}
		return strings.Join(segments[len(segments)-depth:], "/")
	}

	keys := map[string]string{}
	for depth := 1; len(keys) < len(paths); depth++ {
		counts := map[string]int{}
		for _, path := range paths {
			counts[suffix(path, depth)]++
		}
		for _, path := range paths {
			if _, done := keys[path]; done {
				continue
			}
			if s := suffix(path, depth); counts[s] == 1 || s == path {
				keys[path] = s
			}
		}
	}
	return keys
}

func setEntry(modules map[string]map[string]Entry, module, variant string, entry Entry) {
	if modules[module] == nil {
		modules[module] = map[string]Entry{}
	}
	modules[module][variant] = entry
}

// Merge combines an earlier step run's map (base) with the current run's
// (overlay), so several producer steps in one workflow accumulate a single
// document. Entries are matched by module and variant and merged field-wise
// (see mergeEntries): an apk-building and an aab-building run of one variant
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
		AAR:     unionSorted(base.Unmatched.AAR, overlay.Unmatched.AAR),
		Mapping: unionSorted(base.Unmatched.Mapping, overlay.Unmatched.Mapping),
	}

	moduleAARs := map[string][]string{}
	for module, names := range base.ModuleAARs {
		moduleAARs[module] = names
	}
	for module, names := range overlay.ModuleAARs {
		moduleAARs[module] = unionSorted(moduleAARs[module], names)
	}
	if len(moduleAARs) == 0 {
		moduleAARs = nil
	}

	sources := map[string]string{}
	for name, source := range base.Sources {
		sources[name] = source
	}
	for name, source := range overlay.Sources {
		sources[name] = source
	}

	merged := Map{Version: Version, Modules: modules, ModuleAARs: moduleAARs, Unmatched: unmatched, Sources: sources}
	merged.pruneSources()
	return merged, warnings
}

// mergeEntries combines one variant's entries field-wise: what the overlay
// produced wins, what it didn't produce survives from the base. Every
// replacement is reported, even a re-listing of identical values — the noise
// is not worth an equality check.
func mergeEntries(base, overlay Entry, label string) (Entry, []string) {
	var warnings []string
	merged := overlay

	rebuiltApps := false
	if len(overlay.APK) == 0 {
		merged.APK = base.APK
	} else if len(base.APK) != 0 {
		rebuiltApps = true
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its APKs, the artifact map now references the newer files", label))
	}

	if len(overlay.AAB) == 0 {
		merged.AAB = base.AAB
	} else if len(base.AAB) != 0 {
		rebuiltApps = true
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its AABs, the artifact map now references the newer files", label))
	}

	if overlay.Mapping == "" {
		merged.Mapping = base.Mapping
		if rebuiltApps && base.Mapping != "" {
			warnings = append(warnings, fmt.Sprintf(
				"variant %s: the rebuilt app artifacts are paired with the earlier run's mapping file %s, which may be stale", label, base.Mapping))
		}
	} else if base.Mapping != "" && base.Mapping != overlay.Mapping {
		warnings = append(warnings, fmt.Sprintf(
			"variant %s: a later step rebuilt its mapping file, the artifact map now references %s instead of %s",
			label, overlay.Mapping, base.Mapping))
	}

	return merged, warnings
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

// ReplaceFile renames a file reference wherever it appears and reports whether
// anything was replaced. A step that renames an artifact in the deploy dir
// (signing) uses it to keep the map current. Mapping references are left
// alone: artifact transforms don't touch them. Changed lists are reallocated,
// never mutated in place — a merged map's slices may still be shared with the
// document it was merged from.
func (m *Map) ReplaceFile(oldName, newName string) bool {
	replaced := false
	replaceIn := func(list []string) []string {
		changed := false
		out := make([]string, len(list))
		for i, name := range list {
			out[i] = name
			if name == oldName {
				out[i] = newName
				changed = true
			}
		}
		if !changed {
			return list
		}
		sort.Strings(out)
		replaced = true
		return out
	}
	for module, variants := range m.Modules {
		for variant, entry := range variants {
			entry.APK = replaceIn(entry.APK)
			entry.AAB = replaceIn(entry.AAB)
			m.Modules[module][variant] = entry
		}
	}
	for module, names := range m.ModuleAARs {
		m.ModuleAARs[module] = replaceIn(names)
	}
	m.Unmatched.APK = replaceIn(m.Unmatched.APK)
	m.Unmatched.AAB = replaceIn(m.Unmatched.AAB)
	m.Unmatched.AAR = replaceIn(m.Unmatched.AAR)
	if replaced {
		if source, ok := m.Sources[oldName]; ok {
			delete(m.Sources, oldName)
			m.Sources[newName] = source
		}
	}
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
	if len(m.ModuleAARs) > 0 {
		return false
	}
	return len(m.Unmatched.APK) == 0 && len(m.Unmatched.AAB) == 0 && len(m.Unmatched.AAR) == 0 && len(m.Unmatched.Mapping) == 0
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

// Write marshals the map and writes it to path.
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

// ErrNewerVersion marks a document written by a newer producer than this
// package understands. A step merging on write must leave such a file
// untouched instead of replacing it.
var ErrNewerVersion = errors.New("artifact map schema version is newer than this package understands")

// Read loads and validates a map written by Write. It rejects documents with a
// newer schema version than this package understands (ErrNewerVersion), and
// documents that are not artifact maps at all (missing version).
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
		return Map{}, fmt.Errorf("artifact map %s has schema version %d, this step understands up to %d: %w", path, m.Version, Version, ErrNewerVersion)
	}
	m.normalize()
	return m, nil
}

// normalize replaces absent lists with empty ones: a foreign same-version
// document may omit them, and nil round-trips as JSON null, which breaks
// consumers indexing the lists (jq's .apk[] errors on null).
func (m *Map) normalize() {
	if m.Modules == nil {
		m.Modules = map[string]map[string]Entry{}
	}
	for module, variants := range m.Modules {
		for variant, entry := range variants {
			if entry.APK == nil {
				entry.APK = []string{}
			}
			if entry.AAB == nil {
				entry.AAB = []string{}
			}
			m.Modules[module][variant] = entry
		}
	}
	if m.Unmatched.APK == nil {
		m.Unmatched.APK = []string{}
	}
	if m.Unmatched.AAB == nil {
		m.Unmatched.AAB = []string{}
	}
	if m.Unmatched.AAR == nil {
		m.Unmatched.AAR = []string{}
	}
	if m.Unmatched.Mapping == nil {
		m.Unmatched.Mapping = []string{}
	}
}
