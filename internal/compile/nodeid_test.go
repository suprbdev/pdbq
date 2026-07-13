package compile

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestDecodeNodeIDPrecision(t *testing.T) {
	// int8 key above 2^53: must survive decode exactly (UseNumber).
	id := "9007199254740993"
	raw := base64.StdEncoding.EncodeToString([]byte(`["User",` + id + `]`))
	typeName, keys, err := decodeNodeID(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typeName != "User" || len(keys) != 1 {
		t.Fatalf("got %s %v", typeName, keys)
	}
	n, ok := keys[0].(json.Number)
	if !ok || n.String() != id {
		t.Fatalf("key = %#v, want json.Number %s", keys[0], id)
	}
}

func TestDecodeNodeIDWhitespaceTolerant(t *testing.T) {
	// PG's jsonb text rendering inserts a space after the comma.
	raw := base64.StdEncoding.EncodeToString([]byte(`["Post", 1]`))
	typeName, keys, err := decodeNodeID(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typeName != "Post" || len(keys) != 1 {
		t.Fatalf("got %s %v", typeName, keys)
	}
}

func TestDecodeCursorValDualMode(t *testing.T) {
	legacy := base64.StdEncoding.EncodeToString([]byte("o:7"))
	cv, err := decodeCursorVal(legacy)
	if err != nil || cv.keyset || cv.offset != 7 {
		t.Fatalf("legacy cursor: %+v err=%v", cv, err)
	}
	ks := base64.StdEncoding.EncodeToString([]byte(`["Post",3]`))
	cv, err = decodeCursorVal(ks)
	if err != nil || !cv.keyset || cv.typeName != "Post" {
		t.Fatalf("keyset cursor: %+v err=%v", cv, err)
	}
	if _, err := decodeCursorVal("!!!"); err == nil {
		t.Fatal("garbage cursor should error")
	}
	if _, err := decodeCursorVal(base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))); err == nil {
		t.Fatal("non-array JSON cursor should error")
	}
}

func TestKeysetPredicateComposite(t *testing.T) {
	// Composite nodeId: all PK values must decode and count.
	raw := base64.StdEncoding.EncodeToString([]byte(`["Thing","a",2]`))
	typeName, keys, err := decodeNodeID(raw)
	if err != nil || typeName != "Thing" || len(keys) != 2 {
		t.Fatalf("composite decode: %s %v %v", typeName, keys, err)
	}
}
