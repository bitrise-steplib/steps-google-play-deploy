// Package pairing pairs Android app artifacts with the R8 mapping file that
// deobfuscates them, using only the bytes of each file.
//
// R8 stamps a marker into every artifact it compiles and writes a matching
// identifier into the header of the mapping file it emits for that run. Matching
// the two needs no knowledge of build variants, module layout, or file names, so
// it survives the flat deploy directory, collision renames, and re-signing.
package pairing

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// mapIDHeader is the line R8 writes into a mapping file, e.g.
//
//	# pg_map_id: 8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec
var mapIDHeader = regexp.MustCompile(`^#\s*pg_map_id:\s*([0-9a-fA-F]+)\s*$`)

// marker matches R8's embedded marker, e.g.
//
//	~~R8{"backend":"dex","pg-map-id":"8dbc20b3…","version":"8.12.22"}
//
// D8 writes the same shape with a ~~D8 prefix and no pg-map-id.
var marker = regexp.MustCompile(`~~(R8|D8)(\{[^}]*\})`)

// markerFields is the subset of the marker we care about.
type markerFields struct {
	Backend string `json:"backend"`
	MapID   string `json:"pg-map-id"`
	Version string `json:"version"`
}

// Marker is one R8/D8 compilation that contributed to an artifact.
type Marker struct {
	Tool    string // "R8" or "D8"
	Backend string // "dex" for an app's own compilation, "cf" for a minified library
	MapID   string
	Version string
}

// OwnMarker returns the marker describing the artifact's *own* compilation.
//
// An artifact can carry several markers: its own, plus one for every minified
// library baked into it. The own compilation is the one whose backend matches the
// artifact's container format — "dex" for an APK or AAB, "cf" for an AAR. Library
// markers must not be used, or an app would be paired with a library's mapping.
//
// Returns ok=false when the artifact carries no R8 marker with an id, which means
// it was not minified and therefore has no mapping file to find.
func OwnMarker(markers []Marker, wantBackend string) (Marker, bool) {
	for _, m := range markers {
		if m.Tool == "R8" && m.Backend == wantBackend && m.MapID != "" {
			return m, true
		}
	}
	return Marker{}, false
}

// BackendForArtifact returns the marker backend that denotes an artifact's own
// compilation, given its file extension.
func BackendForArtifact(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".aar", ".jar":
		return "cf"
	default: // .apk, .aab
		return "dex"
	}
}

// MapIDFromMapping reads the pg_map_id out of a mapping file's header. The header
// sits in the first handful of lines, so this stops as soon as the comment block
// ends rather than scanning a file that can be tens of megabytes.
func MapIDFromMapping(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "#") {
			break // past the header block
		}
		if m := mapIDHeader.FindStringSubmatch(line); m != nil {
			return strings.ToLower(m[1]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read mapping header: %w", err)
	}
	return "", nil
}

// MarkersFromArchive extracts the R8/D8 markers from an APK, AAB, or AAR.
//
// Only the entries that can carry a marker are inflated, and only the first dex
// is read for a dex-backed artifact: R8 writes its marker into the first one, so
// inflating the rest would be wasted I/O on a large app.
func MarkersFromArchive(zr *zip.Reader) ([]Marker, error) {
	var candidates []*zip.File
	for _, f := range zr.File {
		switch {
		case isFirstDex(f.Name):
			candidates = append(candidates, f)
		case strings.HasSuffix(f.Name, "classes.jar"): // AAR
			candidates = append(candidates, f)
		}
	}
	// Deterministic order, and the shallowest dex first (base/dex/classes.dex).
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })

	var out []Marker
	seen := map[string]bool{}
	for _, f := range candidates {
		ms, err := markersFromEntry(f)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			key := m.Tool + "|" + m.Backend + "|" + m.MapID
			if !seen[key] {
				seen[key] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// isFirstDex reports whether the entry is the primary classes.dex, at the archive
// root (APK) or under a bundle module (AAB). classes2.dex and friends are skipped.
func isFirstDex(name string) bool {
	return path.Base(name) == "classes.dex"
}

func markersFromEntry(f *zip.File) ([]Marker, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer rc.Close()

	// A nested classes.jar has to be searched entry by entry.
	if strings.HasSuffix(f.Name, "classes.jar") {
		buf, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		return markersFromJar(buf)
	}

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name, err)
	}
	return parseMarkers(data), nil
}

func markersFromJar(buf []byte) ([]Marker, error) {
	zr, err := zip.NewReader(newByteReaderAt(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("open nested jar: %w", err)
	}
	var out []Marker
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		if ms := parseMarkers(data); len(ms) > 0 {
			out = append(out, ms...)
		}
	}
	return out, nil
}

func parseMarkers(data []byte) []Marker {
	var out []Marker
	for _, m := range marker.FindAllSubmatch(data, -1) {
		var f markerFields
		if err := json.Unmarshal(m[2], &f); err != nil {
			continue // not a marker we understand; ignore rather than fail
		}
		out = append(out, Marker{
			Tool:    string(m[1]),
			Backend: f.Backend,
			MapID:   strings.ToLower(f.MapID),
			Version: f.Version,
		})
	}
	return out
}
