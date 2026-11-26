package geoip

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oschwald/maxminddb-golang"
)

// A minimal, valid MaxMind DB builder used to give the tests a real
// *maxminddb.Reader without depending on the network or on a committed
// multi-megabyte .mmdb fixture.
//
// The layout maxminddb.FromBytes expects is:
//
//	[search tree][16-byte data section separator][data section]
//	[14-byte metadata marker][metadata map]
//
// The tests that use this only need Open() to succeed so the readers are
// non-nil — they never perform a lookup against the contents. So the search
// tree is a single all-zero node and the data section is empty.

// mmdbMetadataMarker must match the marker maxminddb scans for. Its length
// (14) is also used to compute the data section end offset.
var mmdbMetadataMarker = []byte("\xAB\xCD\xEFMaxMind.com")

// The MaxMind DB type numbers this fixture needs. Only types >= 8 can use the
// "extended" encoding, because a control byte's top 3 bits hold the type and
// 3 bits cannot express anything above 7.
const (
	mmdbTypeUint16 = 5
	mmdbTypeUint32 = 6
	mmdbTypeMap    = 7
	mmdbTypeUint64 = 9
	mmdbTypeArray  = 11
)

// mmdbControl builds a control byte: the top 3 bits are the type, the low 5
// bits carry the payload size for types whose size fits in 5 bits.
func mmdbControl(typ byte, size int) byte {
	return typ<<5 | byte(size)
}

// mmdbString encodes a UTF-8 string (type 2).
func mmdbString(s string) []byte {
	out := []byte{mmdbControl(2, len(s))}
	return append(out, s...)
}

// mmdbUint encodes an unsigned integer using the narrowest type that fits.
// uint16 (5) and uint32 (6) are direct types whose size lives in the control
// byte, and they cap at 2 and 4 payload bytes respectively. Anything larger
// falls back to uint64 (9), which is an extended type and so carries its size
// in the control byte and its real type in the following byte.
//
// Zero is encoded as a zero-length uint16 rather than as one zero byte: a
// leading-zero strip would otherwise report a size of 8, which no uint16 or
// uint32 can express.
func mmdbUint(v uint64) []byte {
	if v == 0 {
		return []byte{mmdbControl(mmdbTypeUint16, 0)}
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	// BigEndian stores the least-significant byte last, so leading zeros must
	// be stripped from the front.
	n := 0
	for n < 8 && tmp[n] == 0 {
		n++
	}
	switch {
	case n <= 2:
		return append([]byte{mmdbControl(mmdbTypeUint16, n)}, tmp[8-n:]...)
	case n <= 4:
		return append([]byte{mmdbControl(mmdbTypeUint32, n)}, tmp[8-n:]...)
	default:
		// Extended encoding: low 5 bits of the control byte hold the size,
		// the next byte holds the real type as (type - 7).
		return append([]byte{byte(n), mmdbTypeUint64 - 7}, tmp[8-n:]...)
	}
}

// mmdbMap encodes a map (type 7). Size fits in the low 5 bits because this
// fixture has fewer than 29 keys.
func mmdbMap(keys []string, values [][]byte) []byte {
	out := []byte{mmdbControl(mmdbTypeMap, len(keys))}
	for i, k := range keys {
		out = append(out, mmdbString(k)...)
		out = append(out, values[i]...)
	}
	return out
}

// mmdbSlice encodes an array (type 11). Because 11 exceeds 7, the encoding is
// "extended": the control byte's top 3 bits are 0 and its low 5 bits hold the
// element count, while the byte after it carries the real type as (type - 7).
func mmdbSlice(items []string) []byte {
	out := []byte{byte(len(items)), mmdbTypeArray - 7}
	for _, s := range items {
		out = append(out, mmdbString(s)...)
	}
	return out
}

// buildMinimalMMDB returns a byte slice that maxminddb.Open accepts. The
// database type is "stub" — geoip uses the reader directly via
// maxminddb-golang rather than geoip2-golang precisely because it never
// validates database_type, so no real database name is needed.
func buildMinimalMMDB() []byte {
	// node_count 1 with record_size 24 gives a 6-byte search tree, which is
	// the smallest non-empty tree the reader will accept.
	const nodeCount = 1
	const recordSize = 24
	searchTreeSize := nodeCount * recordSize / 4

	keys := []string{
		"binary_format_major_version",
		"binary_format_minor_version",
		"build_epoch",
		"database_type",
		"description",
		"ip_version",
		"languages",
		"node_count",
		"record_size",
	}
	values := [][]byte{
		// binary_format_major_version
		mmdbUint(2),
		// binary_format_minor_version
		mmdbUint(0),
		// build_epoch
		mmdbUint(0),
		// database_type
		mmdbString("stub"),
		// description
		mmdbMap([]string{"en"}, [][]byte{mmdbString("geoip test stub")}),
		// ip_version
		mmdbUint(6),
		// languages
		mmdbSlice([]string{"en"}),
		// node_count
		mmdbUint(nodeCount),
		// record_size
		mmdbUint(recordSize),
	}

	out := make([]byte, 0, searchTreeSize+16+len(mmdbMetadataMarker)+64)
	// empty search tree
	out = append(out, make([]byte, searchTreeSize)...)
	// data section separator
	out = append(out, make([]byte, 16)...)
	// empty data section
	out = append(out, make([]byte, 0)...)
	out = append(out, mmdbMetadataMarker...)
	out = append(out, mmdbMap(keys, values)...)
	return out
}

// serveMinimalMMDB starts an httptest server that responds to every request
// with a minimal but valid MaxMind DB, then points the package-level database
// URLs at it. Both the server and the URL override are undone by t.Cleanup, so
// the caller needs no defer of its own.
//
// This keeps the tests hermetic: without it, Open() and Update() contact the
// real CDN, and ensureFile carries a 5-minute context timeout, so a sandboxed
// run blocks until the Go test timeout fires.
func serveMinimalMMDB(t *testing.T) {
	t.Helper()
	body := buildMinimalMMDB()
	digest := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = fmt.Fprintf(w, "%x  db.mmdb\n", digest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(pointURLsAt(srv.URL, "/db.mmdb"))
}

// serveDownloadFailure points the database URLs at a local server that always
// answers 500, so the download-failure path is exercised without waiting on a
// real CDN timeout. Like serveMinimalMMDB, the server and the URL override are
// undone by t.Cleanup.
func serveDownloadFailure(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(pointURLsAt(srv.URL, "/db.mmdb"))
}

// TestBuildMinimalMMDB_OpensInReader guards the fixture itself. The MMDB
// encoding is hand-rolled here, and every field of it is positional: one wrong
// type number or one size in the wrong byte shifts the rest of the map and the
// reader fails with a message that points at the data section rather than at
// the real cause. Asserting the fixture opens keeps that failure local to this
// helper instead of surfacing as a confusing error in each test that uses it.
func TestBuildMinimalMMDB_OpensInReader(t *testing.T) {
	r, err := maxminddb.FromBytes(buildMinimalMMDB())
	if err != nil {
		t.Fatalf("buildMinimalMMDB produced an unreadable database: %v", err)
	}
	if r.Metadata.RecordSize != 24 {
		t.Errorf("RecordSize = %d; want 24", r.Metadata.RecordSize)
	}
	if r.Metadata.NodeCount != 1 {
		t.Errorf("NodeCount = %d; want 1", r.Metadata.NodeCount)
	}
	if r.Metadata.IPVersion != 6 {
		t.Errorf("IPVersion = %d; want 6", r.Metadata.IPVersion)
	}
}
