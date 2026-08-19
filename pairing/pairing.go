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
	"bytes"
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
var marker = regexp.MustCompile(`~~(R8|D8)(\{[^}]{0,512}\})`)

// maxMarkerLen bounds the overlap kept between scan chunks, so a marker split
// across a chunk boundary is still matched. It must exceed the longest marker the
// regex above can match.
const maxMarkerLen = 1024

// markerFields is the subset of the marker we care about.
type markerFields struct {
	Backend string `json:"backend"`
	MapID   string `json:"pg-map-id"`
	Version string `json:"version"`
}

// Marker is one R8/D8 compilation that contributed to an artifact.
type Marker struct {
	Tool string // "R8" or "D8"
	// Backend is "dex" for a compilation targeting an app, "cf" for one targeting
	// class files. Nothing selects on it — see MapIDs — but it is recorded because
	// it is what a report about an artifact carrying several ids would need.
	Backend string
	MapID   string
	Version string
}

// MapIDs returns the distinct pg_map_ids of the R8 compilations that produced the
// artifact, in the order they were found.
//
// Normally there is exactly one. R8 allows an artifact to carry more than one
// marker — one per R8 run that contributed to it, so a minified library baked into
// an app could add its own — but that could not be reproduced: building an app
// against a minified library module produced a single marker whether or not the app
// itself was minified, because R8 or D8 recompiles the library's classes and stamps
// its own.
//
// Callers treat more than one as ambiguous and skip pairing rather than guess.
// Uploading a library's mapping against an app's version code would be silently and
// permanently wrong, whereas skipping is visible and recoverable.
func MapIDs(markers []Marker) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range markers {
		if m.Tool != "R8" || m.MapID == "" {
			continue // D8, or an R8 run that produced no mapping
		}
		if !seen[m.MapID] {
			seen[m.MapID] = true
			ids = append(ids, m.MapID)
		}
	}
	return ids
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

// MarkersFromArchive extracts the R8/D8 markers from an APK or AAB.
//
// Only the primary dex of the archive root or of each bundle module is read: R8
// writes its marker into the first dex, so inflating the rest would be wasted I/O
// on a large app.
func MarkersFromArchive(zr *zip.Reader) ([]Marker, error) {
	var candidates []*zip.File
	for _, f := range zr.File {
		if isPrimaryDex(f.Name) {
			candidates = append(candidates, f)
		}
	}
	// Deterministic order. Note this is byte order, not shallowest-first: for an AAB
	// whose feature module sorts before "base" the feature dex is read first. That
	// only affects the order of the returned markers.
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

// isPrimaryDex reports whether the entry is a primary classes.dex that belongs to
// the app itself: at the archive root for an APK, or directly under a bundle
// module's dex/ directory for an AAB.
//
// Anchoring matters. A bare basename check would also match a dex shipped as an
// asset — assets/patch/classes.dex, res/raw/classes.dex, as hot-patch frameworks
// produce — whose markers belong to something other than this app and would make
// the artifact look ambiguous. classes2.dex and friends are skipped either way.
func isPrimaryDex(name string) bool {
	if name == "classes.dex" {
		return true // APK
	}
	// AAB: <module>/dex/classes.dex
	parts := strings.Split(name, "/")
	return len(parts) == 3 && parts[1] == "dex" && parts[2] == "classes.dex"
}

func markersFromEntry(f *zip.File) ([]Marker, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	ms, err := scanMarkers(rc)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", f.Name, err)
	}
	return ms, nil
}

// scanMarkers finds markers in a stream without materialising it.
//
// The whole entry is still read: measured marker offsets in real R8/D8 output are
// 66-92% of the dex, always in the last third, so there is no prefix of the file
// that can be scanned instead. Nor can it stop at the first marker, because
// detecting an artifact that carries several ids requires seeing all of them.
//
// What this avoids is holding the entry in memory. A dex is routinely 10-20 MB and
// only a short marker string is needed, so the stream is read in fixed chunks with
// an overlap long enough that a marker spanning a boundary is still matched. See
// the benchmarks in scan_test.go for the numbers.
func scanMarkers(r io.Reader) ([]Marker, error) {
	const chunk = 256 * 1024

	var (
		out  []Marker
		seen = map[string]bool{}
		buf  = make([]byte, 0, chunk+maxMarkerLen)
		tmp  = make([]byte, chunk)
	)

	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for _, m := range parseMarkers(buf) {
				key := m.Tool + "|" + m.Backend + "|" + m.MapID
				if !seen[key] {
					seen[key] = true
					out = append(out, m)
				}
			}
			// Keep a tail long enough to catch a marker split across the boundary.
			if len(buf) > maxMarkerLen {
				buf = append(buf[:0], buf[len(buf)-maxMarkerLen:]...)
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func parseMarkers(data []byte) []Marker {
	if !bytes.Contains(data, []byte("~~")) {
		return nil // cheap reject for the overwhelming majority of chunks
	}
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

// unusedPathImport keeps the path import honest if isPrimaryDex is ever rewritten
// in terms of path.Base again.
var _ = path.Base
