package pairing

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
)

// Index maps a pg_map_id to the mapping files carrying it.
//
// The value is a slice because byte-identical mapping files share an id: two
// build variants that produce the same bytecode produce the same mapping, so any
// of them deobfuscates the artifact correctly.
type Index map[string][]string

// Logger is the subset of the step's logger this package needs.
type Logger interface {
	Printf(format string, v ...interface{})
	Warnf(format string, v ...interface{})
}

// BuildIndex reads the pg_map_id header of every candidate mapping file.
//
// A candidate that cannot be read, or that carries no pg_map_id header, is
// reported and skipped rather than failing the deploy: it simply cannot be paired
// with anything.
func BuildIndex(candidates []string, logger Logger) Index {
	idx := Index{}
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
			logger.Warnf("Skipping %s: no '# pg_map_id:' header, so it cannot be matched to an artifact. Is it an R8/ProGuard mapping file?", path)
			continue
		}
		idx[id] = append(idx[id], path)
	}
	return idx
}

// ForArtifact returns the mapping file that deobfuscates the given app artifact.
//
// It reads the R8 marker out of the artifact itself, so the result does not depend
// on file names, directory layout, or the order of the candidate list — all of
// which are lost by the time artifacts reach the deploy directory.
//
// The three outcomes are distinct on purpose:
//   - a mapping file: pair it with this artifact's version code;
//   - "" with needsMapping false: the artifact is not minified, so there is
//     nothing to upload and nothing is wrong;
//   - "" with needsMapping true: the artifact is minified but no candidate
//     matches, which is worth a warning.
func (idx Index) ForArtifact(artifactPath string, logger Logger) (mapping string, needsMapping bool, err error) {
	zr, err := zip.OpenReader(artifactPath)
	if err != nil {
		return "", false, fmt.Errorf("open %s: %w", artifactPath, err)
	}
	defer func() { _ = zr.Close() }()

	markers, err := MarkersFromArchive(&zr.Reader)
	if err != nil {
		return "", false, fmt.Errorf("read compiler markers from %s: %w", artifactPath, err)
	}

	own, ok := OwnMarker(markers, BackendForArtifact(artifactPath))
	if !ok {
		// No R8 marker with a map id: compiled by D8, or not minified at all.
		return "", false, nil
	}

	paths := idx[own.MapID]
	if len(paths) == 0 {
		return "", true, nil
	}
	if len(paths) > 1 {
		logger.Printf("  %d mapping files share id %s; they are byte-identical for pairing purposes, using %s", len(paths), short(own.MapID), paths[0])
	}
	return paths[0], true, nil
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12] + "..."
	}
	return id
}
