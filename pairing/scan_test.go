package pairing

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// readAllScan is the previous implementation, kept so the streaming scan can be
// proved equivalent to it.
//
// The marker cannot be found without reading essentially the whole dex: measured
// offsets in real output are 66-92% of the file, always in the last third, because
// R8 stores the marker as a string constant and dex string data sits late in the
// layout. So this is about bounding memory, not about reading less. On a 20 MB dex:
//
//	ReadAll   1.63 ms   50.3 MB allocated   57 allocs
//	chunked   0.85 ms    0.58 MB allocated   18 allocs
//
// ReadAll allocates more than the file size because its buffer grows by repeated
// doubling; avoiding that copying is also why the streaming version is faster.
func readAllScan(r io.Reader) ([]Marker, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return parseMarkers(data), nil
}

// synthetic dex: marker at ~90% of the file, as measured on real output.
func syntheticDex(size int) []byte {
	var b bytes.Buffer
	at := size * 9 / 10
	b.Write(bytes.Repeat([]byte("x"), at))
	b.WriteString(realAPKMarker)
	b.Write(bytes.Repeat([]byte("y"), size-at))
	return b.Bytes()
}

func BenchmarkReadAll_20MB(b *testing.B) {
	dex := syntheticDex(20 << 20)
	b.SetBytes(int64(len(dex)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := readAllScan(bytes.NewReader(dex)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChunked_20MB(b *testing.B) {
	dex := syntheticDex(20 << 20)
	b.SetBytes(int64(len(dex)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := scanMarkers(bytes.NewReader(dex)); err != nil {
			b.Fatal(err)
		}
	}
}

// Both must find the same marker, or the benchmark is meaningless.
func TestChunkedMatchesReadAll(t *testing.T) {
	for _, size := range []int{1 << 10, 300 << 10, 5 << 20} {
		dex := syntheticDex(size)
		a, err := readAllScan(bytes.NewReader(dex))
		if err != nil {
			t.Fatal(err)
		}
		c, err := scanMarkers(bytes.NewReader(dex))
		if err != nil {
			t.Fatal(err)
		}
		if len(MapIDs(a)) != 1 || len(MapIDs(c)) != 1 || MapIDs(a)[0] != MapIDs(c)[0] {
			t.Errorf("size %d: readAll=%v chunked=%v", size, MapIDs(a), MapIDs(c))
		}
	}
	// And a marker split exactly across the boundary.
	pad := strings.Repeat("x", 256*1024-len(realAPKMarker)/2)
	c, err := scanMarkers(strings.NewReader(pad + realAPKMarker))
	if err != nil {
		t.Fatal(err)
	}
	if len(MapIDs(c)) != 1 {
		t.Errorf("boundary-split marker missed: %v", MapIDs(c))
	}
}
