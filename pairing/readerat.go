package pairing

import "io"

// byteReaderAt adapts a byte slice to io.ReaderAt for archive/zip.
type byteReaderAt struct{ b []byte }

func newByteReaderAt(b []byte) io.ReaderAt { return byteReaderAt{b} }

func (r byteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
