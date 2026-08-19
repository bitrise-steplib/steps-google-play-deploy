package pairing

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(f string, v ...interface{}) { l.t.Logf("  "+f, v...) }
func (l testLogger) Debugf(f string, v ...interface{}) { l.t.Logf("D "+f, v...) }
func (l testLogger) Warnf(f string, v ...interface{})  { l.t.Logf("W "+f, v...) }

// realMappingHeader is the header R8 8.12.22 writes, taken verbatim from a build of
// bitrise-io/android-multiple-test-results-sample with AGP 8.12.3.
const realMappingHeader = `# compiler: R8
# compiler_version: 8.12.22
# min_api: 26
# common_typos_disable
# {"id":"com.android.tools.r8.mapping","version":"2.2"}
# pg_map_id: 8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec
# pg_map_hash: SHA-256 8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec
android.app.AppComponentFactory -> android.app.AppComponentFactory:
`

func TestMapIDFromMapping(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "real R8 8.12.22 header",
			content: realMappingHeader,
			want:    "8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec",
		},
		{
			name:    "short id, as older R8 versions emit",
			content: "# compiler: R8\n# pg_map_id: 58e175b\nfoo -> a:\n",
			want:    "58e175b",
		},
		{
			name:    "uppercase id is normalised",
			content: "# pg_map_id: ABCDEF01\n",
			want:    "abcdef01",
		},
		{
			name:    "no header, e.g. a Compose mapping",
			content: "some.Class -> a:\n",
			want:    "",
		},
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
		{
			// The scanner must stop at the end of the comment block rather than
			// reading a mapping file that can be tens of megabytes.
			name:    "id after the header block is ignored",
			content: "# compiler: R8\nfoo -> a:\n# pg_map_id: deadbeef\n",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MapIDFromMapping(strings.NewReader(tt.content))
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapIDs(t *testing.T) {
	// The normal case: one R8 compilation, one id.
	ids := MapIDs([]Marker{{Tool: "R8", Backend: "dex", MapID: "58e175b"}})
	if len(ids) != 1 || ids[0] != "58e175b" {
		t.Errorf("single marker: got %v", ids)
	}

	// D8 means no minification, so there is no mapping to look for.
	if ids := MapIDs([]Marker{{Tool: "D8", Backend: "dex", MapID: ""}}); len(ids) != 0 {
		t.Errorf("a D8 marker must yield no id, got %v", ids)
	}

	// An R8 marker with an empty id is equally unusable.
	if ids := MapIDs([]Marker{{Tool: "R8", Backend: "dex", MapID: ""}}); len(ids) != 0 {
		t.Errorf("an R8 marker without an id must yield nothing, got %v", ids)
	}

	if ids := MapIDs(nil); len(ids) != 0 {
		t.Errorf("no markers must yield no ids, got %v", ids)
	}

	// The same id repeated across dex entries collapses.
	ids = MapIDs([]Marker{
		{Tool: "R8", Backend: "dex", MapID: "aaa"},
		{Tool: "R8", Backend: "dex", MapID: "aaa"},
	})
	if len(ids) != 1 {
		t.Errorf("duplicate ids must collapse, got %v", ids)
	}

	// Several distinct ids are reported as such; the caller decides what to do.
	ids = MapIDs([]Marker{
		{Tool: "R8", Backend: "cf", MapID: "5da8853"},
		{Tool: "R8", Backend: "dex", MapID: "58e175b"},
	})
	if len(ids) != 2 {
		t.Errorf("expected both ids reported, got %v", ids)
	}
}

func TestForArtifactAmbiguousIsSkippedNotGuessed(t *testing.T) {
	// Two distinct R8 ids in one artifact. Uploading either could attach a
	// library's mapping to the app's version code, so nothing is uploaded.
	dir := t.TempDir()
	apk := writeZip(t, dir, "app-release.apk", map[string]string{
		"classes.dex": `~~R8{"backend":"cf","pg-map-id":"5da8853","version":"8.12.22"}` + "\x00" +
			`~~R8{"backend":"dex","pg-map-id":"58e175b","version":"8.12.22"}`,
	})
	m1 := writeFile(t, dir, "a.txt", "# compiler: R8\n# pg_map_id: 5da8853\n")
	m2 := writeFile(t, dir, "b.txt", "# compiler: R8\n# pg_map_id: 58e175b\n")

	idx := BuildIndex([]string{m1, m2}, testLogger{t})
	mapping, outcome, err := idx.ForArtifact(apk, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if mapping != "" {
		t.Errorf("ambiguous artifact must not be paired, got %q", mapping)
	}
	if outcome != Ambiguous {
		t.Errorf("ambiguity must be reported as Ambiguous, not %v — the caller has to be able to warn about it", outcome)
	}
}

func TestBuildIndexGroupsByID(t *testing.T) {
	dir := t.TempDir()
	// Two byte-identical mappings, as two variants with the same bytecode produce.
	// They share an id, so either one deobfuscates either artifact.
	a := writeFile(t, dir, "mapping.txt", realMappingHeader)
	b := writeFile(t, dir, "mapping20260819185216.txt", realMappingHeader)
	// A Compose mapping: no pg_map_id, must be skipped rather than break the index.
	c := writeFile(t, dir, "compose-mapping.txt", "com.example.Foo -> a:\n")

	idx := BuildIndex([]string{a, b, c, filepath.Join(dir, "does-not-exist.txt")}, testLogger{t})

	if idx.Len() != 1 {
		t.Fatalf("expected 1 id, got %d", idx.Len())
	}
	if idx.WithoutID != 1 {
		t.Errorf("expected the Compose mapping to be counted as id-less, got %d", idx.WithoutID)
	}
	paths := idx.byID["8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec"]
	if len(paths) != 2 {
		t.Errorf("expected both identical mappings under one id, got %v", paths)
	}
}

func TestMarkersFromArchive(t *testing.T) {
	// A dex entry carrying both the app's own marker and a baked-in library's.
	dex := []byte("junk\x00" +
		`~~R8{"backend":"cf","pg-map-id":"5da8853","version":"8.12.22"}` + "\x00" +
		`~~R8{"backend":"dex","pg-map-id":"58e175b","version":"8.12.22"}` + "\x00trailer")

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range map[string][]byte{
		"classes.dex":         dex,
		"classes2.dex":        []byte(`~~R8{"backend":"dex","pg-map-id":"ffffff","version":"8.12.22"}`),
		"AndroidManifest.xml": []byte("not a dex"),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	markers, err := MarkersFromArchive(zr)
	if err != nil {
		t.Fatal(err)
	}

	if len(markers) != 2 {
		t.Fatalf("expected the 2 markers from classes.dex only, got %d: %+v", len(markers), markers)
	}
	// classes2.dex must not be read: R8 stamps the first dex, so the rest is
	// wasted I/O on a large app.
	for _, m := range markers {
		if m.MapID == "ffffff" {
			t.Error("classes2.dex was read; only the first dex should be")
		}
	}

	ids := MapIDs(markers)
	if len(ids) != 2 {
		t.Errorf("expected both ids from classes.dex, got %v", ids)
	}
}

func TestForArtifactNotMinified(t *testing.T) {
	dir := t.TempDir()
	apk := writeZip(t, dir, "app-debug.apk", map[string]string{
		"classes.dex": `~~D8{"backend":"dex","compilation-mode":"debug","version":"8.12.22"}`,
	})

	idx := BuildIndex([]string{writeFile(t, dir, "mapping.txt", realMappingHeader)}, testLogger{t})
	mapping, outcome, err := idx.ForArtifact(apk, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NotMinified || mapping != "" {
		t.Errorf("a D8 artifact needs no mapping: got mapping=%q outcome=%v", mapping, outcome)
	}
}

func TestForArtifactMinifiedButUnmatched(t *testing.T) {
	dir := t.TempDir()
	apk := writeZip(t, dir, "app-release.apk", map[string]string{
		"classes.dex": `~~R8{"backend":"dex","pg-map-id":"nomatch","version":"8.12.22"}`,
	})

	idx := BuildIndex([]string{writeFile(t, dir, "mapping.txt", realMappingHeader)}, testLogger{t})
	mapping, outcome, err := idx.ForArtifact(apk, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Unmatched {
		t.Errorf("a minified artifact with no matching candidate must be Unmatched, got %v", outcome)
	}
	if mapping != "" {
		t.Errorf("no candidate matches, want empty, got %q", mapping)
	}
}

func TestForArtifactPairsByContentNotName(t *testing.T) {
	dir := t.TempDir()
	// The collision-renamed name carries no hint of which variant it belongs to,
	// which is exactly the situation in the deploy directory.
	renamed := writeFile(t, dir, "mapping20260819185217.txt", realMappingHeader)
	apk := writeZip(t, dir, "app-full-release-unsigned.apk", map[string]string{
		"classes.dex": `~~R8{"backend":"dex","pg-map-id":"8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec","version":"8.12.22"}`,
	})

	idx := BuildIndex([]string{renamed}, testLogger{t})
	mapping, outcome, err := idx.ForArtifact(apk, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Paired || mapping != renamed {
		t.Errorf("got mapping=%q outcome=%v, want %q paired", mapping, outcome, renamed)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeZip(t *testing.T, dir, name string, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for n, c := range entries {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildIndexDedupesDuplicatePaths(t *testing.T) {
	// The mapping_file default names both BITRISE_MAPPING_PATH_LIST and
	// BITRISE_MAPPING_PATH, so the same file commonly arrives twice.
	dir := t.TempDir()
	p := writeFile(t, dir, "mapping.txt", realMappingHeader)

	idx := BuildIndex([]string{p, p, filepath.Join(dir, ".", "mapping.txt")}, testLogger{t})

	paths := idx.byID["8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec"]
	if len(paths) != 1 {
		t.Errorf("expected the duplicate to collapse to 1 candidate, got %v", paths)
	}
}

// realAPKMarker is the marker R8 8.12.22 embedded in classes.dex of a real APK
// built from bitrise-io/android-multiple-test-results-sample with AGP 8.12.3,
// copied verbatim. Its pg-map-id is byte-for-byte the pg_map_id in
// realMappingHeader above, which is the assumption the whole feature rests on:
// the two sides agree, at the same width.
const realAPKMarker = `~~R8{"backend":"dex","compilation-mode":"release","has-checksums":false,` +
	`"min-api":26,"pg-map-id":"8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec",` +
	`"r8-mode":"full","version":"8.12.22"}`

// realCompatAPKMarker is the same, from a build with android.enableR8.fullMode=false.
// R8 writes the id in compatibility mode too, so the mode is never a reason to fail
// to pair.
const realCompatAPKMarker = `~~R8{"backend":"dex","compilation-mode":"release","has-checksums":false,` +
	`"min-api":26,"pg-map-id":"41c5d2304e0257577019d29214901eff3f473638914ef868189845db2f6d34ab",` +
	`"r8-mode":"compatibility","version":"8.12.22"}`

const realCompatMappingHeader = `# compiler: R8
# compiler_version: 8.12.22
# pg_map_id: 41c5d2304e0257577019d29214901eff3f473638914ef868189845db2f6d34ab
foo -> a:
`

// TestRealMarkerAndHeaderAgree is the load-bearing test: the id in the artifact
// marker and the id in the mapping header must be equal, at equal width, for real
// R8 output. Both fixtures are verbatim from real builds.
func TestRealMarkerAndHeaderAgree(t *testing.T) {
	for _, tt := range []struct {
		name, marker, header string
	}{
		{"R8 full mode", realAPKMarker, realMappingHeader},
		{"R8 compatibility mode", realCompatAPKMarker, realCompatMappingHeader},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			apk := writeZip(t, dir, "app-release.apk", map[string]string{"classes.dex": tt.marker})
			mapping := writeFile(t, dir, "mapping.txt", tt.header)

			idx := BuildIndex([]string{mapping}, testLogger{t})
			got, outcome, err := idx.ForArtifact(apk, testLogger{t})
			if err != nil {
				t.Fatal(err)
			}
			if outcome != Paired || got != mapping {
				t.Errorf("real R8 output did not pair: mapping=%q outcome=%v", got, outcome)
			}
		})
	}
}

// TestEachArtifactGetsItsOwnMapping is the PR's headline behaviour: N artifacts,
// N mapping files, each paired with its own regardless of order or file name.
func TestEachArtifactGetsItsOwnMapping(t *testing.T) {
	dir := t.TempDir()

	idA := "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	idB := "1111aaaa2222bbbb3333cccc4444dddd5555eeee6666ffff7777aaaa8888bbbb"

	apkA := writeZip(t, dir, "app-demo-release-unsigned.apk", map[string]string{
		"classes.dex": `~~R8{"backend":"dex","pg-map-id":"` + idA + `","version":"8.12.22"}`,
	})
	apkB := writeZip(t, dir, "app-full-release-unsigned.apk", map[string]string{
		"classes.dex": `~~R8{"backend":"dex","pg-map-id":"` + idB + `","version":"8.12.22"}`,
	})

	// Deploy-directory reality: neither name says which variant it belongs to, and
	// the list order is the reverse of the artifact order.
	mapB := writeFile(t, dir, "mapping20260819185217.txt", "# compiler: R8\n# pg_map_id: "+idB+"\nfoo -> a:\n")
	mapA := writeFile(t, dir, "mapping.txt", "# compiler: R8\n# pg_map_id: "+idA+"\nfoo -> a:\n")

	idx := BuildIndex([]string{mapB, mapA}, testLogger{t})
	if idx.Len() != 2 {
		t.Fatalf("expected 2 distinct ids, got %d", idx.Len())
	}

	for _, tt := range []struct{ apk, want string }{{apkA, mapA}, {apkB, mapB}} {
		got, outcome, err := idx.ForArtifact(tt.apk, testLogger{t})
		if err != nil {
			t.Fatal(err)
		}
		if outcome != Paired || got != tt.want {
			t.Errorf("%s: got %s (%v), want %s", filepath.Base(tt.apk), filepath.Base(got), outcome, filepath.Base(tt.want))
		}
	}
}

func TestLookupPrefixMatching(t *testing.T) {
	full := "8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec"

	t.Run("short marker id against full header id", func(t *testing.T) {
		dir := t.TempDir()
		mapping := writeFile(t, dir, "mapping.txt", "# compiler: R8\n# pg_map_id: "+full+"\nfoo -> a:\n")
		apk := writeZip(t, dir, "app.apk", map[string]string{
			"classes.dex": `~~R8{"backend":"dex","pg-map-id":"8dbc20b","version":"8.12.22"}`,
		})
		idx := BuildIndex([]string{mapping}, testLogger{t})
		got, outcome, err := idx.ForArtifact(apk, testLogger{t})
		if err != nil {
			t.Fatal(err)
		}
		if outcome != Paired || got != mapping {
			t.Errorf("a truncated marker id must still pair: got %q (%v)", got, outcome)
		}
	})

	t.Run("ambiguous prefix is not paired", func(t *testing.T) {
		dir := t.TempDir()
		// Two mappings sharing the marker's prefix: pairing either could be wrong.
		m1 := writeFile(t, dir, "a.txt", "# compiler: R8\n# pg_map_id: 8dbc20b111\nfoo -> a:\n")
		m2 := writeFile(t, dir, "b.txt", "# compiler: R8\n# pg_map_id: 8dbc20b222\nfoo -> a:\n")
		apk := writeZip(t, dir, "app.apk", map[string]string{
			"classes.dex": `~~R8{"backend":"dex","pg-map-id":"8dbc20b","version":"8.12.22"}`,
		})
		idx := BuildIndex([]string{m1, m2}, testLogger{t})
		got, outcome, err := idx.ForArtifact(apk, testLogger{t})
		if err != nil {
			t.Fatal(err)
		}
		if outcome != Unmatched || got != "" {
			t.Errorf("an ambiguous prefix must not be paired: got %q (%v)", got, outcome)
		}
	})
}

func TestIsPrimaryDexIgnoresAssetDexes(t *testing.T) {
	for name, want := range map[string]bool{
		"classes.dex":                    true,  // APK
		"base/dex/classes.dex":           true,  // AAB base module
		"feature/dex/classes.dex":        true,  // AAB feature module
		"classes2.dex":                   false, // secondary dex
		"assets/patch/classes.dex":       false, // hot-patch framework
		"res/raw/classes.dex":            false,
		"base/assets/tinker/classes.dex": false,
		"lib/arm64-v8a/classes.dex":      false,
	} {
		if got := isPrimaryDex(name); got != want {
			t.Errorf("isPrimaryDex(%q) = %v, want %v", name, got, want)
		}
	}
}

// A dex shipped as an asset carries markers that belong to something other than
// this app; counting them would make the artifact look ambiguous and silently cost
// it its mapping file.
func TestAssetDexDoesNotCauseAmbiguity(t *testing.T) {
	dir := t.TempDir()
	id := "8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec"
	apk := writeZip(t, dir, "app-release.apk", map[string]string{
		"classes.dex":              `~~R8{"backend":"dex","pg-map-id":"` + id + `","version":"8.12.22"}`,
		"assets/patch/classes.dex": `~~R8{"backend":"dex","pg-map-id":"deadbeefdeadbeef","version":"8.12.22"}`,
	})
	mapping := writeFile(t, dir, "mapping.txt", "# compiler: R8\n# pg_map_id: "+id+"\nfoo -> a:\n")

	idx := BuildIndex([]string{mapping}, testLogger{t})
	got, outcome, err := idx.ForArtifact(apk, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Paired || got != mapping {
		t.Errorf("an asset dex must not affect pairing: got %q (%v)", got, outcome)
	}
}

// A marker straddling the chunk boundary of the streaming scan must still be found.
func TestScanMarkersAcrossChunkBoundary(t *testing.T) {
	// 256 KiB chunk; place the marker so it spans the first boundary.
	const chunk = 256 * 1024
	pad := strings.Repeat("x", chunk-len(realAPKMarker)/2)
	ms, err := scanMarkers(strings.NewReader(pad + realAPKMarker + "tail"))
	if err != nil {
		t.Fatal(err)
	}
	ids := MapIDs(ms)
	if len(ids) != 1 || ids[0] != "8dbc20b32dec8d41286f5392325c991ae32bea7cf590a3031165ab39b8b6aaec" {
		t.Errorf("marker spanning a chunk boundary was not found: %v", ids)
	}
}
