// Minimal read-only reader for the SQLite on-disk format.
//
// Devin CLI persists per-session usage totals in a SQLite database
// (~/.local/share/devin/cli/sessions.db, see upstream rust/adapters/devin).
// The Go standard library ships no SQL driver, so this file implements just
// enough of the format to locate one table in sqlite_master and stream its
// rows: file header, table b-trees (interior + leaf pages), overflow chains,
// and record decoding. See https://www.sqlite.org/fileformat2.html.
//
// Limitations (by design): databases in WAL mode are read from their last
// checkpoint, and WITHOUT ROWID tables (stored as index b-trees) are not
// scanned. Neither occurs in Devin CLI session databases in practice.
package devin

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
)

// sqliteFile is a parsed view of one SQLite database image.
type sqliteFile struct {
	data     []byte
	pageSize int
	usable   int // pageSize minus reserved bytes per page
	encoding int // 1 = UTF-8, 2 = UTF-16LE, 3 = UTF-16BE
	pages    int // number of complete pages in the file
}

// openSQLite validates the database header of a raw file image.
func openSQLite(data []byte) (*sqliteFile, error) {
	const header = "SQLite format 3\x00"
	if len(data) < 100 || string(data[:len(header)]) != header {
		return nil, fmt.Errorf("not a sqlite v3 database")
	}
	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("invalid sqlite page size %d", pageSize)
	}
	reserved := int(data[20])
	usable := pageSize - reserved
	if usable < 480 {
		return nil, fmt.Errorf("invalid sqlite usable page size %d", usable)
	}
	enc := int(binary.BigEndian.Uint32(data[56:60]))
	if enc == 0 {
		enc = 1 // legacy databases may leave the field zeroed; UTF-8 is the default
	}
	if enc < 1 || enc > 3 {
		return nil, fmt.Errorf("unsupported sqlite text encoding %d", enc)
	}
	return &sqliteFile{
		data:     data,
		pageSize: pageSize,
		usable:   usable,
		encoding: enc,
		pages:    len(data) / pageSize,
	}, nil
}

// page returns the raw bytes of page n (1-based).
func (db *sqliteFile) page(n int) ([]byte, error) {
	if n < 1 || n > db.pages {
		return nil, fmt.Errorf("sqlite page %d out of range", n)
	}
	off := (n - 1) * db.pageSize
	return db.data[off : off+db.pageSize], nil
}

// tableInfo describes one user table located in sqlite_master.
type tableInfo struct {
	root    int
	columns []string
	// rowidAlias is the index of an INTEGER PRIMARY KEY column, whose value
	// is stored as NULL in records and must be read from the rowid. -1 when
	// the table has no such column.
	rowidAlias int
}

// findTable locates a table by (case-insensitive) name. A nil result with a
// nil error means the database has no such table.
func (db *sqliteFile) findTable(name string) (*tableInfo, error) {
	var info *tableInfo
	err := db.walkTable(1, func(_ int64, payload []byte) error { // page 1 roots sqlite_master
		vals, err := db.parseRecord(payload)
		if err != nil {
			return err
		}
		if len(vals) < 5 {
			return nil
		}
		typ, _ := vals[0].(string)
		tname, _ := vals[1].(string)
		if typ != "table" || !strings.EqualFold(tname, name) {
			return nil
		}
		root, ok := asInt(vals[3])
		if !ok || root < 1 {
			return nil
		}
		sql, _ := vals[4].(string)
		cols, pk := parseCreateTable(sql)
		info = &tableInfo{root: int(root), columns: cols, rowidAlias: pk}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// rows streams every record of the table rooted at ti.root in rowid order.
// Records are padded/truncated to the column count; each value is nil,
// int64, float64, string, or []byte.
func (db *sqliteFile) rows(ti *tableInfo, visit func(rowid int64, vals []any) error) error {
	return db.walkTable(ti.root, func(rowid int64, payload []byte) error {
		vals, err := db.parseRecord(payload)
		if err != nil {
			return err
		}
		for len(vals) < len(ti.columns) {
			vals = append(vals, nil) // ALTER TABLE ADD COLUMN leaves old rows short
		}
		if ti.rowidAlias >= 0 && ti.rowidAlias < len(vals) {
			if vals[ti.rowidAlias] == nil {
				vals[ti.rowidAlias] = rowid // INTEGER PRIMARY KEY lives in the rowid
			}
		}
		return visit(rowid, vals)
	})
}

// walkTable performs an in-order traversal of a table b-tree, calling visit
// with each leaf record payload (without its rowid).
func (db *sqliteFile) walkTable(root int, visit func(rowid int64, payload []byte) error) error {
	visited := make(map[int]bool)
	var walk func(n int, depth int) error
	walk = func(n int, depth int) error {
		if depth > 64 {
			return fmt.Errorf("sqlite b-tree too deep at page %d", n)
		}
		if visited[n] {
			return fmt.Errorf("sqlite b-tree cycle at page %d", n)
		}
		visited[n] = true
		page, err := db.page(n)
		if err != nil {
			return err
		}
		hdr := 0
		if n == 1 {
			hdr = 100 // the file header occupies the first 100 bytes of page 1
		}
		switch page[hdr] {
		case 5: // interior table page
			ncells := int(binary.BigEndian.Uint16(page[hdr+3 : hdr+5]))
			ptrs := hdr + 12
			for i := 0; i < ncells; i++ {
				if ptrs+2*i+2 > len(page) {
					return fmt.Errorf("truncated sqlite page %d", n)
				}
				cell := int(binary.BigEndian.Uint16(page[ptrs+2*i : ptrs+2*i+2]))
				if cell+4 > len(page) {
					return fmt.Errorf("truncated sqlite cell on page %d", n)
				}
				child := int(binary.BigEndian.Uint32(page[cell : cell+4]))
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			rightmost := int(binary.BigEndian.Uint32(page[hdr+8 : hdr+12]))
			return walk(rightmost, depth+1)
		case 13: // leaf table page
			ncells := int(binary.BigEndian.Uint16(page[hdr+3 : hdr+5]))
			ptrs := hdr + 8
			for i := 0; i < ncells; i++ {
				if ptrs+2*i+2 > len(page) {
					return fmt.Errorf("truncated sqlite page %d", n)
				}
				cell := int(binary.BigEndian.Uint16(page[ptrs+2*i : ptrs+2*i+2]))
				rowid, payload, err := db.leafCell(page, cell)
				if err != nil {
					return fmt.Errorf("sqlite page %d: %w", n, err)
				}
				if err := visit(rowid, payload); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unexpected sqlite page type %d on page %d", page[hdr], n)
		}
	}
	return walk(root, 0)
}

// leafCell decodes one table-leaf cell: payload-length varint, rowid varint,
// local payload, and an overflow chain when the payload does not fit locally.
func (db *sqliteFile) leafCell(page []byte, off int) (int64, []byte, error) {
	size, n, err := readVarint(page, off)
	if err != nil {
		return 0, nil, err
	}
	off += n
	rowid, n, err := readVarint(page, off)
	if err != nil {
		return 0, nil, err
	}
	off += n
	if size < 0 || size > int64(len(db.data)) {
		return 0, nil, fmt.Errorf("implausible sqlite payload size %d", size)
	}
	local := int(size)
	if maxLocal := db.usable - 35; int(size) > maxLocal {
		minLocal := ((db.usable-12)*32)/255 - 23
		k := minLocal + (int(size)-minLocal)%(db.usable-4)
		if k <= maxLocal {
			local = k
		} else {
			local = minLocal
		}
	}
	if off+local > len(page) {
		return 0, nil, fmt.Errorf("truncated sqlite cell payload")
	}
	payload := make([]byte, 0, size)
	payload = append(payload, page[off:off+local]...)
	if local < int(size) {
		if off+local+4 > len(page) {
			return 0, nil, fmt.Errorf("missing sqlite overflow pointer")
		}
		next := int(binary.BigEndian.Uint32(page[off+local : off+local+4]))
		remaining := int(size) - local
		for remaining > 0 {
			if next == 0 {
				return 0, nil, fmt.Errorf("broken sqlite overflow chain")
			}
			opage, err := db.page(next)
			if err != nil {
				return 0, nil, err
			}
			next = int(binary.BigEndian.Uint32(opage[0:4]))
			chunk := db.usable - 4
			if chunk > remaining {
				chunk = remaining
			}
			if 4+chunk > len(opage) {
				return 0, nil, fmt.Errorf("truncated sqlite overflow page")
			}
			payload = append(payload, opage[4:4+chunk]...)
			remaining -= chunk
		}
	}
	return rowid, payload, nil
}

// parseRecord decodes a record payload into its column values.
func (db *sqliteFile) parseRecord(payload []byte) ([]any, error) {
	headerLen, n, err := readVarint(payload, 0)
	if err != nil || headerLen < int64(n) || headerLen > int64(len(payload)) {
		return nil, fmt.Errorf("corrupt sqlite record header")
	}
	types := make([]int64, 0, 8)
	for off := n; off < int(headerLen); {
		t, m, err := readVarint(payload, off)
		if err != nil {
			return nil, err
		}
		types = append(types, t)
		off += m
	}
	vals := make([]any, 0, len(types))
	body := int(headerLen)
	for _, t := range types {
		size := serialSize(t)
		if size < 0 || body+size > len(payload) {
			vals = append(vals, nil) // tolerate short bodies instead of failing the scan
			continue
		}
		v, err := decodeValue(db.encoding, t, payload[body:body+size])
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
		body += size
	}
	return vals, nil
}

// serialSize returns the body byte count for a serial type, or -1 when the
// type is reserved.
func serialSize(t int64) int {
	switch {
	case t == 0 || t == 8 || t == 9:
		return 0
	case t >= 1 && t <= 4:
		return int(t)
	case t == 5:
		return 6
	case t == 6 || t == 7:
		return 8
	case t >= 12 && t%2 == 0:
		return int(t-12) / 2
	case t >= 13:
		return int(t-13) / 2
	default:
		return -1
	}
}

func decodeValue(enc int, t int64, b []byte) (any, error) {
	switch {
	case t == 0:
		return nil, nil
	case t >= 1 && t <= 6:
		return signedBE(b), nil
	case t == 7:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case t == 8:
		return int64(0), nil
	case t == 9:
		return int64(1), nil
	case t >= 12 && t%2 == 0:
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil
	case t >= 13:
		return decodeText(enc, b), nil
	default:
		return nil, fmt.Errorf("reserved sqlite serial type %d", t)
	}
}

// signedBE interprets a 1..8 byte big-endian two's-complement integer.
func signedBE(b []byte) int64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	if len(b) > 0 && len(b) < 8 && b[0]&0x80 != 0 {
		v |= ^uint64(0) << (uint(len(b)) * 8)
	}
	return int64(v)
}

func decodeText(enc int, b []byte) string {
	switch enc {
	case 2, 3:
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			if enc == 2 {
				u = append(u, binary.LittleEndian.Uint16(b[i:i+2]))
			} else {
				u = append(u, binary.BigEndian.Uint16(b[i:i+2]))
			}
		}
		return string(utf16.Decode(u))
	default:
		return string(b)
	}
}

// readVarint decodes the 1–9 byte big-endian varint at b[off].
func readVarint(b []byte, off int) (int64, int, error) {
	var v uint64
	for i := 0; i < 9; i++ {
		if off+i >= len(b) {
			return 0, 0, fmt.Errorf("truncated sqlite varint")
		}
		c := b[off+i]
		if i == 8 {
			v = v<<8 | uint64(c)
			return int64(v), 9, nil
		}
		v = v<<7 | uint64(c&0x7f)
		if c&0x80 == 0 {
			return int64(v), i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("unreachable sqlite varint")
}

// parseCreateTable extracts column names from a CREATE TABLE statement and
// the index of an INTEGER PRIMARY KEY column (-1 when absent). Table-level
// constraints are skipped. It is a tolerant parser: anything unrecognized
// yields whatever columns were understood.
func parseCreateTable(sql string) ([]string, int) {
	open := strings.IndexByte(sql, '(')
	if open < 0 {
		return nil, -1
	}
	close_, ok := matchParen(sql, open)
	if !ok {
		return nil, -1
	}
	body := sql[open+1 : close_]
	var cols []string
	pk := -1
	for _, def := range splitTopLevel(body) {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		name, rest, ok := leadingIdentifier(def)
		if !ok {
			continue
		}
		lower := strings.ToLower(name)
		switch lower {
		case "constraint", "primary", "unique", "check", "foreign":
			continue // table-level constraint, not a column
		}
		tokens := strings.Fields(strings.ToLower(rest))
		for i := range tokens {
			tokens[i] = strings.Trim(tokens[i], ",()") // "key," from column lists
		}
		isInteger := len(tokens) > 0 && tokens[0] == "integer"
		hasPK := false
		for i := 0; i+1 < len(tokens); i++ {
			if tokens[i] == "primary" && tokens[i+1] == "key" {
				hasPK = true
				break
			}
		}
		if isInteger && hasPK && pk == -1 {
			pk = len(cols)
		}
		cols = append(cols, lower)
	}
	return cols, pk
}

// matchParen returns the index of the parenthesis closing the one at open,
// honoring SQL quoting.
func matchParen(sql string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(sql); i++ {
		switch c := sql[i]; c {
		case '\'', '"', '`':
			j := i + 1
			for j < len(sql) {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c { // escaped quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
		case '[':
			j := strings.IndexByte(sql[i+1:], ']')
			if j < 0 {
				return 0, false
			}
			i += j + 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// splitTopLevel splits on commas not nested in parentheses or quotes.
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"', '`':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
		case '[':
			j := strings.IndexByte(s[i+1:], ']')
			if j >= 0 {
				i += j + 1
			}
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// leadingIdentifier extracts a (possibly quoted) identifier from the start of
// s and returns the remainder.
func leadingIdentifier(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	switch q := s[0]; q {
	case '"', '`', '\'':
		for i := 1; i < len(s); i++ {
			if s[i] == q {
				if i+1 < len(s) && s[i+1] == q {
					continue // doubled quote escapes itself; kept verbatim
				}
				return s[1:i], s[i+1:], true
			}
		}
		return "", "", false
	case '[':
		if j := strings.IndexByte(s, ']'); j > 0 {
			return s[1:j], s[j+1:], true
		}
		return "", "", false
	}
	end := len(s)
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '(' || s[i] == ',' {
			end = i
			break
		}
	}
	return s[:end], s[end:], end > 0
}

// asInt converts a decoded value to int64 when it is numeric.
func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	default:
		return 0, false
	}
}
