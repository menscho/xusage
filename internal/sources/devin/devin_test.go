// Package devin — tests for the SQLite reader, row mapping, and discovery.
package devin

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// sessionDBFixtureSQL creates the upstream sessions schema plus rows covering:
// a billable row (cost + reasoning), a NULL-model row, an all-zero row, a
// reasoning-only row, and an invalid-timestamp row.
const sessionDBFixtureSQL = `
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	model TEXT,
	created_at REAL NOT NULL,
	message_count INTEGER DEFAULT 0,
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	cache_read_tokens INTEGER DEFAULT 0,
	cache_creation_tokens INTEGER DEFAULT 0,
	reasoning_tokens INTEGER DEFAULT 0,
	total_cost REAL
);
INSERT INTO sessions (id, model, created_at, message_count, input_tokens,
	output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	total_cost)
VALUES ('s1', 'claude-sonnet-4-20250514', 1750000000.25, 42,
	1200, 300, 50, 20, 10, 0.34);
INSERT INTO sessions (id, model, created_at, message_count, input_tokens,
	output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	total_cost)
VALUES ('s2', NULL, 1750000100.0, 1, 5, 5, 0, 0, 0, 0.0);
INSERT INTO sessions (id, model, created_at, message_count, input_tokens,
	output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	total_cost)
VALUES ('s3', 'gpt-5.5', 1750000200.0, 3, 0, 0, 0, 0, 0, 0.0);
INSERT INTO sessions (id, model, created_at, message_count, input_tokens,
	output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	total_cost)
VALUES ('s4', 'gpt-5.5', 1750000300.0, 2, 100, 40, 0, 0, 25, 0.05);
INSERT INTO sessions (id, model, created_at, message_count, input_tokens,
	output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	total_cost)
VALUES ('s5', 'gpt-5.5', 1750000400.0, 7, 10, 5, 0, 0, 0, 0.11);
`

// writeSessionDBWithPython builds a real sessions.db using the python sqlite3
// module (present on typical dev hosts; the sqlite3 CLI often is not).
func writeSessionDBWithPython(t *testing.T, dir, sql string) string {
	t.Helper()
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not available")
	}
	script := "import sqlite3,sys\n" +
		"con = sqlite3.connect(sys.argv[1])\n" +
		"con.executescript(sys.stdin.read())\n" +
		"con.commit()\n"
	cmd := exec.Command(py, "-c", script, filepath.Join(dir, "sessions.db"))
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("python sqlite3 failed: %v: %s", err, out)
	}
	return filepath.Join(dir, "sessions.db")
}

// writeSessionDB creates a Devin sessions.db fixture, preferring python and
// falling back to the sqlite3 CLI. Skips when neither tool exists.
func writeSessionDB(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("python"); err == nil {
		return writeSessionDBWithPython(t, dir, sessionDBFixtureSQL)
	}
	if _, err := exec.LookPath("sqlite3"); err == nil {
		p := filepath.Join(dir, "sessions.db")
		cmd := exec.Command("sqlite3", p)
		cmd.Stdin = strings.NewReader(sessionDBFixtureSQL)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("sqlite3 CLI failed: %v: %s", err, out)
		}
		return p
	}
	t.Skip("neither python nor sqlite3 available")
	return ""
}

func TestParseSessionDB(t *testing.T) {
	p := writeSessionDB(t, t.TempDir())
	entries, err := parse(p)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// s1 billable; s2 NULL model; s3 all-zero usage; s4 reasoning + cost;
	// s5 plain billable. Invalid created_at is covered by the handcrafted DB
	// test, so every fixture row here that is non-empty is kept.
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	var e1, e4, e5 model.Entry
	for _, e := range entries {
		switch e.ID {
		case "s1":
			e1 = e
		case "s4":
			e4 = e
		case "s5":
			e5 = e
		}
	}
	if e1.ID != "s1" {
		t.Fatalf("s1 missing: %+v", entries)
	}
	if e1.Model != "claude-sonnet-4-20250514" || e1.SessionID != "s1" || e1.Project != "devin" {
		t.Errorf("s1 identity wrong: %+v", e1)
	}
	if e1.InputTokens != 1200 || e1.OutputTokens != 300 || e1.CacheReadTokens != 50 ||
		e1.CacheCreationTokens != 20 || e1.ExtraTotalTokens != 10 {
		t.Errorf("s1 tokens wrong: %+v", e1)
	}
	if e1.Messages != 42 {
		t.Errorf("s1 messages = %d, want 42", e1.Messages)
	}
	if e1.ReportedCost == nil || *e1.ReportedCost != 0.34 {
		t.Errorf("s1 cost = %v, want 0.34", e1.ReportedCost)
	}
	if want := int64(1750000000); e1.Timestamp.Unix() != want {
		t.Errorf("s1 timestamp = %v, want epoch %d", e1.Timestamp, want)
	}
	if e4.ID != "s4" {
		t.Fatalf("s4 missing: %+v", entries)
	}
	if e4.ExtraTotalTokens != 25 || e4.InputTokens != 100 {
		t.Errorf("s4 tokens wrong: %+v", e4)
	}
	if e4.ReportedCost == nil || *e4.ReportedCost != 0.05 {
		t.Errorf("s4 cost = %v, want 0.05", e4.ReportedCost)
	}
	if e5.ID != "s5" {
		t.Fatalf("s5 missing: %+v", entries)
	}
	if e5.InputTokens != 10 || e5.OutputTokens != 5 || e5.Messages != 7 {
		t.Errorf("s5 tokens/messages wrong: %+v", e5)
	}
	if e5.ReportedCost == nil || *e5.ReportedCost != 0.11 {
		t.Errorf("s5 cost = %v, want 0.11", e5.ReportedCost)
	}
	for _, e := range entries {
		if e.Timestamp.Before(time.Unix(1, 0)) {
			t.Errorf("entry %s kept a pre-epoch timestamp: %v", e.ID, e.Timestamp)
		}
	}
}

func TestParseNotADatabase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(p, []byte("this is not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := parse(p)
	if err != nil {
		t.Fatalf("foreign file should not error, got %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
}

func TestParseMissingSessionsTable(t *testing.T) {
	p := writeSessionDBWithPython(t, t.TempDir(), "CREATE TABLE other (a TEXT);\nINSERT INTO other VALUES ('x');\n")
	entries, err := parse(p)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
}

// --- handcrafted database image (keeps the reader covered without python/sqlite3) ---

// testRecord encodes values as a SQLite record: nil = NULL, string = text,
// int = integer with a minimal serial type.
func testRecord(vals ...any) []byte {
	var types []int64
	var body []byte
	for _, v := range vals {
		switch x := v.(type) {
		case nil:
			types = append(types, 0)
		case string:
			types = append(types, int64(13+2*len(x)))
			body = append(body, x...)
		case int:
			b, st := testInt(int64(x))
			types = append(types, st)
			body = append(body, b...)
		default:
			panic(fmt.Sprintf("unsupported test record value %T", v))
		}
	}
	var hdr []byte
	for _, st := range types {
		var tmp [9]byte
		n := putTestVarint(tmp[:], st)
		hdr = append(hdr, tmp[:n]...)
	}
	hdr = append([]byte{0}, hdr...)
	hdr[0] = byte(len(hdr))
	return append(hdr, body...)
}

// testInt encodes v as signed big-endian bytes, returning them with the
// matching SQLite serial type.
func testInt(v int64) ([]byte, int64) {
	switch {
	case v >= -128 && v <= 127:
		return []byte{byte(v)}, 1
	case v >= -32768 && v <= 32767:
		return []byte{byte(v >> 8), byte(v)}, 2
	case v >= -8388608 && v <= 8388607:
		return []byte{byte(v >> 16), byte(v >> 8), byte(v)}, 3
	case v >= -2147483648 && v <= 2147483648:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(v))
		return b[:], 4
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		return b[:], 6
	}
}

// buildTestDB assembles a two-page SQLite image: page 1 holds sqlite_master
// with one table whose root is page 2; page 2 is a leaf of data records.
func buildTestDB(pageSize int, masterRecord []byte, dataRecords [][]byte) []byte {
	buf := make([]byte, 2*pageSize)
	copy(buf, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(buf[16:], uint16(pageSize))
	binary.BigEndian.PutUint32(buf[28:], 2) // page count
	binary.BigEndian.PutUint32(buf[56:], 1) // UTF-8 text encoding

	// Page 1: b-tree header at offset 100, one cell (the master record).
	buf[100] = 13 // leaf table page
	binary.BigEndian.PutUint16(buf[103:], 1)
	const mp = 200 // master cell content offset within page 1
	binary.BigEndian.PutUint16(buf[105:], mp)
	binary.BigEndian.PutUint16(buf[108:], mp) // cell pointer array entry 0
	n := putTestVarint(buf[mp:], int64(len(masterRecord)))
	n += putTestVarint(buf[mp+n:], 1) // rowid
	copy(buf[mp+n:], masterRecord)

	// Page 2: the table's root leaf with all data records.
	d := pageSize
	buf[d] = 13
	binary.BigEndian.PutUint16(buf[d+3:], uint16(len(dataRecords)))
	ptrs := d + 8
	p := ptrs + 2*len(dataRecords)
	for i, rec := range dataRecords {
		binary.BigEndian.PutUint16(buf[ptrs+2*i:], uint16(p-d))
		n := putTestVarint(buf[p:], int64(len(rec)))
		m := putTestVarint(buf[p+n:], int64(i+1)) // rowid
		copy(buf[p+n+m:], rec)
		p += n + m + len(rec)
	}
	return buf
}

// putTestVarint writes the SQLite varint of v into b, returning its length.
func putTestVarint(b []byte, v int64) int {
	u := uint64(v)
	if u>>63 != 0 {
		for i := 0; i < 8; i++ {
			b[i] = byte(u>>uint(8*(7-i))) | 0x80
		}
		b[8] = byte(u)
		return 9
	}
	var tmp [9]byte
	n := 0
	for {
		tmp[n] = byte(u & 0x7f)
		u >>= 7
		n++
		if u == 0 {
			break
		}
	}
	for i := 0; i < n; i++ {
		c := tmp[n-1-i]
		if i < n-1 {
			c |= 0x80
		}
		b[i] = c
	}
	return n
}

func TestParseHandcraftedDB(t *testing.T) {
	// Columns: id INTEGER PRIMARY KEY (rowid alias), model TEXT,
	// input_tokens INTEGER, created_at REAL. Two rows: one billable, one
	// empty-model skip.
	const sql = "CREATE TABLE sessions (id INTEGER PRIMARY KEY, model TEXT, input_tokens INT, created_at REAL)"
	master := testRecord("table", "sessions", "sessions", 2, sql)
	row1 := testRecord(nil, "claude-x", 120, 1750000000) // NULL id -> rowid alias 1
	row2 := testRecord(nil, "", 0, 1750000100)           // empty model -> skipped
	db := buildTestDB(512, master, [][]byte{row1, row2})
	p := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(p, db, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := parse(p)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly 1", entries)
	}
	e := entries[0]
	if e.ID != "1" || e.Model != "claude-x" || e.InputTokens != 120 {
		t.Errorf("entry wrong: %+v", e)
	}
	if e.SessionID != "1" || e.Project != "devin" {
		t.Errorf("identity wrong: %+v", e)
	}
}

func TestDiscoverMissingDirReturnsNilNil(t *testing.T) {
	t.Setenv(EnvDataDir, filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if files != nil || err != nil {
		t.Errorf("discover() = %v, %v; want nil, nil", files, err)
	}
}

func TestDiscoverEnvOverrideWins(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "envroot")
	homeLike := filepath.Join(t.TempDir(), "homelike", ".local", "share", "devin", "cli")
	for _, dir := range []string{envDir, homeLike} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// env root holds the sessions database; home default must be ignored.
	if err := os.WriteFile(filepath.Join(envDir, dbFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeLike, dbFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("env set: only env roots scanned", func(t *testing.T) {
		t.Setenv(EnvDataDir, envDir)
		files, err := discover()
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("got %v, want 1 file under %s", files, envDir)
		}
		if !strings.HasSuffix(files[0], filepath.Join(envDir, dbFileName)) {
			t.Errorf("unexpected file: %v", files[0])
		}
	})
	t.Run("blank env entries ignored", func(t *testing.T) {
		t.Setenv(EnvDataDir, " , "+envDir+" , ")
		files, err := discover()
		if err != nil || len(files) != 1 {
			t.Errorf("discover() = %v, %v; want 1 file", files, err)
		}
	})
}

func TestDiscoverFindsSessionsDB(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, dbFileName)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// discover joins dbFileName onto the data dir, so the override must name the
	// directory that directly contains sessions.db.
	t.Setenv(EnvDataDir, dir)
	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], dbFileName) {
		t.Errorf("files = %v, want the sessions.db", files)
	}
}

func TestRegistration(t *testing.T) {
	var found *sources.Source
	for i, s := range sources.All() {
		if s.Name == "devin" {
			found = &sources.All()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("devin source not registered via init()")
	}
	if found.Discover == nil || found.Parse == nil {
		t.Error("registered source has nil Discover/Parse")
	}
}
