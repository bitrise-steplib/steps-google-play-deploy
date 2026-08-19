package pairing

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Index maps a pg_map_id to the mapping files carrying it.
//
// A single id can map to several files because byte-identical mapping files share
// an id: two build variants that produce the same bytecode produce the same
// mapping, so any of them deobfuscates the artifact correctly.
type Index struct {
	byID map[string][]string
	// WithoutID counts candidates that carry no pg_map_id and so cannot be paired
	// by content at all. Non-zero means an obfuscator other than R8 produced them.
	WithoutID int
}

// Logger is the subset of the step's logger this package needs.
type Logger interface {
	Printf(format string, v ...interface{})
	Debugf(format string, v ...interface{})
	Warnf(format string, v ...interface{})
}

// Outcome is what pairing concluded about one artifact.
type Outcome int

const (
	// Paired: a mapping file deobfuscates this artifact.
	Paired Outcome = iota
	// NotMinified: the artifact carries no R8 map id, so there is no mapping to
	// upload and nothing is wrong.
	NotMinified
	// Unmatched: the artifact is minified but no candidate matches it.
	Unmatched
	// Ambiguous: the artifact carries several R8 map ids, so which one describes the
	// app cannot be determined. Deliberately distinct from NotMinified: something
	// *is* wrong here, and the caller must be able to say so.
	Ambiguous
)

// Len returns the number of distinct ids indexed.
func (idx Index) Len() int { return len(idx.byID) }

// BuildIndex reads the pg_map_id header of every candidate mapping file.
//
// A candidate that cannot be read, or that carries no pg_map_id header, is counted
// and skipped rather than failing the deploy: it simply cannot be paired by
// content. Individual skips are logged at debug level, because a non-R8 obfuscator
// produces one per file and that is a supported configuration rather than a
// problem; the caller reports the summary.
func BuildIndex(candidates []string, logger Logger) Index {
	idx := Index{byID: map[string][]string{}}
	seen := map[string]bool{}
	for _, path := range candidates {
		// The default value of the mapping_file input names both
		// BITRISE_MAPPING_PATH_LIST and BITRISE_MAPPING_PATH, so the same file
		// commonly appears twice.
		if abs, err := filepath.Abs(path); err == nil {
			if seen[abs] {
				continue
			}
			seen[abs] = true
		}

		f, err := os.Open(path)
		if err != nil {
			logger.Warnf("Skipping mapping file, cannot open it: %s", err)
			continue
		}
		id, err := MapIDFromMapping(f)
		_ = f.Close()
		if err != nil {
			logger.Warnf("Skipping mapping file %s: %s", path, err)
			continue
		}
		if id == "" {
			logger.Debugf("%s has no '# pg_map_id:' header, so it cannot be matched to an artifact by content", path)
			idx.WithoutID++
			continue
		}
		idx.byID[id] = append(idx.byID[id], path)
	}
	return idx
}

// ForArtifact returns the mapping file that deobfuscates the given app artifact.
//
// It reads the R8 marker out of the artifact itself, so the result does not depend
// on file names, directory layout, or the order of the candidate list — all of
// which are lost by the time artifacts reach the deploy directory.
func (idx Index) ForArtifact(artifactPath string, logger Logger) (string, Outcome, error) {
	zr, err := zip.OpenReader(artifactPath)
	if err != nil {
		return "", Unmatched, fmt.Errorf("open %s: %w", artifactPath, err)
	}
	defer func() { _ = zr.Close() }()

	markers, err := MarkersFromArchive(&zr.Reader)
	if err != nil {
		return "", Unmatched, fmt.Errorf("read compiler markers from %s: %w", artifactPath, err)
	}

	ids := MapIDs(markers)
	switch len(ids) {
	case 0:
		return "", NotMinified, nil
	case 1:
	default:
		logger.Debugf("%s markers: %+v", filepath.Base(artifactPath), markers)
		return "", Ambiguous, nil
	}

	paths, matchedID := idx.lookup(ids[0])
	if len(paths) == 0 {
		return "", Unmatched, nil
	}
	if matchedID != ids[0] {
		logger.Debugf("matched artifact id %s against mapping id %s by prefix", short(ids[0]), short(matchedID))
	}
	if len(paths) > 1 {
		logger.Printf("  %d mapping files share id %s; they are byte-identical for pairing purposes, using %s", len(paths), short(matchedID), paths[0])
	}
	return paths[0], Paired, nil
}

// lookup resolves an artifact's map id against the index.
//
// Exact match first. Failing that, one side may be a truncated form of the other:
// R8 has emitted both a short hash prefix and the full SHA-256 as the map id, and
// nothing guarantees the artifact marker and the mapping header will use the same
// width forever. A prefix match is accepted only when it is unique, so an ambiguous
// abbreviation is treated as no match rather than paired with the wrong file.
func (idx Index) lookup(artifactID string) (paths []string, matchedID string) {
	if p, ok := idx.byID[artifactID]; ok {
		return p, artifactID
	}

	var matches []string
	for id := range idx.byID {
		if strings.HasPrefix(id, artifactID) || strings.HasPrefix(artifactID, id) {
			matches = append(matches, id)
		}
	}
	if len(matches) != 1 {
		return nil, ""
	}
	return idx.byID[matches[0]], matches[0]
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12] + "..."
	}
	return id
}
