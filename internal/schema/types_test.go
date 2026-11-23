package schema_test

import (
	"testing"

	"github.com/suprbdev/pdbq/internal/schema"
)

func TestObjectFieldIndexTracksDirectSliceEdits(t *testing.T) {
	o := &schema.Object{Name: "T"}
	o.AddField(&schema.Field{Name: "a"})
	o.AddField(&schema.Field{Name: "b"})
	if o.Field("b") == nil || o.Field("c") != nil {
		t.Fatal("lookup after AddField")
	}
	o.Fields = append(o.Fields, &schema.Field{Name: "c"})
	if o.Field("c") == nil {
		t.Fatal("directly appended field not found")
	}
	repl := &schema.Field{Name: "b", Type: "X"}
	o.AddField(repl)
	if len(o.Fields) != 3 || o.Field("b") != repl {
		t.Fatal("AddField must replace in place")
	}
	o.Fields = []*schema.Field{o.Fields[2], o.Fields[0]}
	if o.Field("b") != nil || o.Field("c") == nil || o.Field("a") == nil {
		t.Fatal("lookup after truncation")
	}
	o.RemoveField("c")
	if o.Field("c") != nil || o.Field("a") == nil || len(o.Fields) != 1 {
		t.Fatal("RemoveField")
	}
	o.Fields = append(o.Fields, &schema.Field{Name: "a", Type: "dup"})
	if o.Field("a").Type == "dup" {
		t.Fatal("first field of a name must win")
	}
}

func TestInputFieldIndex(t *testing.T) {
	in := &schema.Input{Name: "I"}
	in.AddField(&schema.InputField{Name: "x"})
	in.AddField(&schema.InputField{Name: "y"})
	in.AddField(&schema.InputField{Name: "x", Type: "T"})
	if len(in.Fields) != 2 || in.Field("x").Type != "T" {
		t.Fatal("AddField replace")
	}
	in.RemoveField("x")
	if in.Field("x") != nil || in.Field("y") == nil {
		t.Fatal("RemoveField")
	}
}

func TestEnumHasValue(t *testing.T) {
	e := &schema.Enum{Name: "E"}
	if e.HasValue("A") {
		t.Fatal("empty enum")
	}
	e.Values = append(e.Values, schema.EnumValue{Name: "A"}, schema.EnumValue{Name: "B"})
	if !e.HasValue("B") || e.HasValue("C") {
		t.Fatal("lookup")
	}
	e.Values = e.Values[:1]
	if e.HasValue("B") || !e.HasValue("A") {
		t.Fatal("lookup after truncation")
	}
}
