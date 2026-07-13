package compile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprbdev/pdbq/internal/introspect"
)

// Global identifiers and keyset cursors share one format: base64 of the JSON
// array ["TypeName", pk...]. They are decoded structurally (JSON) and never
// compared textually — jsonb's text rendering inserts spaces ("["Post", 1]")
// and that must stay harmless on both the Go and SQL side.

// nodeIDSQL renders the SQL expression producing a row's nodeId. The replace
// strips the newlines PostgreSQL's base64 encoder inserts every 76 chars
// (composite or uuid keys exceed that).
func nodeIDSQL(typeName string, t *introspect.Table, alias string) string {
	parts := []string{quoteLiteral(typeName) + "::text"}
	for _, col := range t.PrimaryKey.Columns {
		parts = append(parts, alias+"."+quoteIdent(col))
	}
	return fmt.Sprintf("replace(encode(convert_to(jsonb_build_array(%s)::text, 'UTF8'), 'base64'), E'\\n', '')",
		strings.Join(parts, ", "))
}

// decodeNodeID parses a nodeId into its type name and primary key values.
// UseNumber keeps int8 keys beyond 2^53 exact; numeric keys surface as
// json.Number and are passed to the driver as text.
func decodeNodeID(s string) (string, []any, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("compile: malformed nodeId")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil || len(arr) < 2 {
		return "", nil, fmt.Errorf("compile: malformed nodeId")
	}
	typeName, ok := arr[0].(string)
	if !ok || typeName == "" {
		return "", nil, fmt.Errorf("compile: malformed nodeId")
	}
	return typeName, arr[1:], nil
}

// cursorVal is a decoded pagination cursor: either a legacy/PK-less offset
// cursor ("o:<n>") or a keyset cursor (nodeId format).
type cursorVal struct {
	keyset   bool
	offset   int
	typeName string
	keys     []any
}

// decodeCursorVal accepts both cursor formats.
func decodeCursorVal(cur string) (cursorVal, error) {
	raw, err := base64.StdEncoding.DecodeString(cur)
	if err != nil {
		return cursorVal{}, fmt.Errorf("compile: malformed cursor")
	}
	str := string(raw)
	if strings.HasPrefix(str, "o:") {
		n, err := decodeCursor(cur)
		if err != nil {
			return cursorVal{}, err
		}
		return cursorVal{offset: n}, nil
	}
	typeName, keys, err := decodeNodeID(cur)
	if err != nil {
		return cursorVal{}, fmt.Errorf("compile: malformed cursor")
	}
	return cursorVal{keyset: true, typeName: typeName, keys: keys}, nil
}

// keyParam converts a decoded JSON key value into a driver parameter,
// applying column coercion (enums, jsonb) on top of json.Number handling.
func (s *stmt) keyParam(col *introspect.Column, v any) any {
	if n, ok := v.(json.Number); ok {
		return s.coerce(col, n.String())
	}
	return s.coerce(col, v)
}

// keyCondsFromValues builds `alias."col" = $n` for decoded key values.
func (s *stmt) keyCondsFromValues(t *introspect.Table, cols []string, vals []any, alias string) ([]string, error) {
	if len(cols) != len(vals) {
		return nil, fmt.Errorf("compile: nodeId has %d key values, want %d", len(vals), len(cols))
	}
	conds := make([]string, 0, len(cols))
	for i, cn := range cols {
		c := t.Column(cn)
		if c == nil {
			return nil, fmt.Errorf("compile: unknown key column %q", cn)
		}
		conds = append(conds, fmt.Sprintf("%s.%s = %s", alias, quoteIdent(cn), s.param(s.keyParam(c, vals[i]))))
	}
	return conds, nil
}
