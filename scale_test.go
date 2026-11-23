package pdbq_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

const pg = "pg_catalog"

func col(name string, pos int, typ string) *introspect.Column {
	return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: pg, BaseTypeSchema: pg}
}

// genCatalog synthesises an n-table catalog without a database: 20 columns
// per table, 3 indexes, and 2-3 foreign keys per table. With hub set every
// table also references table 0; otherwise the targets are random.
func genCatalog(n int, hub bool) *introspect.Catalog {
	rng := rand.New(rand.NewSource(1))
	priv := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	cat := &introspect.Catalog{
		FormatVersion: 1, Schemas: []string{"public"},
		Extensions: []*introspect.Extension{{Name: "postgis", Schema: "public"}},
	}
	types := []string{"text", "int4", "int8", "bool", "timestamptz", "jsonb", "numeric", "uuid"}
	for i := range n {
		t := &introspect.Table{
			Schema: "public", Name: fmt.Sprintf("t%04d", i), Kind: introspect.RelTable, Privileges: priv,
			PrimaryKey: &introspect.Constraint{Name: fmt.Sprintf("t%04d_pkey", i), Columns: []string{"id"}},
		}
		id := col("id", 1, "int4")
		id.NotNull, id.GeneratedKind = true, introspect.GeneratedIdentityAlways
		t.Columns = append(t.Columns, id)
		for c := 2; c <= 20; c++ {
			cc := col(fmt.Sprintf("c%02d", c), c, types[rng.Intn(len(types))])
			cc.NotNull = c%3 == 0
			t.Columns = append(t.Columns, cc)
		}
		if i%7 == 0 {
			loc := col("loc", len(t.Columns)+1, "geometry")
			loc.TypeSchema, loc.BaseTypeSchema = "public", "public"
			loc.Spatial = &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326}
			t.Columns = append(t.Columns, loc)
			t.Indexes = append(t.Indexes, &introspect.Index{Name: fmt.Sprintf("t%04d_loc_idx", i), Columns: []string{"loc"}, Method: "gist"})
		}
		targets := map[int]bool{}
		if hub && i != 0 {
			targets[0] = true
		}
		for len(targets) < 2+rng.Intn(2) {
			if j := rng.Intn(n); j != i {
				targets[j] = true
			}
		}
		fk := 0
		for j := range n {
			if !targets[j] {
				continue
			}
			name := fmt.Sprintf("ref%d", fk)
			fk++
			ref := col(name+"_id", 0, "int4")
			ref.Position = len(t.Columns) + 1
			t.Columns = append(t.Columns, ref)
			t.ForeignKeys = append(t.ForeignKeys, &introspect.ForeignKey{
				Name: fmt.Sprintf("t%04d_%s_fkey", i, name), Columns: []string{ref.Name},
				RefSchema: "public", RefTable: fmt.Sprintf("t%04d", j), RefColumns: []string{"id"},
			})
			t.Indexes = append(t.Indexes, &introspect.Index{Name: fmt.Sprintf("t%04d_%s_idx", i, name), Columns: []string{ref.Name}, Method: "btree"})
		}
		t.Indexes = append(t.Indexes,
			&introspect.Index{Name: fmt.Sprintf("t%04d_pkey", i), Columns: []string{"id"}, Unique: true, Method: "btree"},
			&introspect.Index{Name: fmt.Sprintf("t%04d_c02_idx", i), Columns: []string{"c02"}, Method: "btree"},
		)
		cat.Tables = append(cat.Tables, t)
		if i%5 == 0 {
			cat.Functions = append(cat.Functions, &introspect.Function{
				Schema: "public", Name: fmt.Sprintf("f%04d", i), ReturnType: "int4", ReturnTypeSchema: pg,
				Args: []introspect.FuncArg{{Name: "x", PGType: "int4"}},
			})
		}
	}
	return cat
}

func scaleApp(indexedOnly, aggregates, simple bool) *pdbq.App {
	cfg := config.DefaultConfig()
	cfg.Filters.IndexedOnly = indexedOnly
	cfg.Plugins.Settings.SimpleNames.Enabled = simple
	if aggregates {
		cfg.Aggregates.Enabled = true
	}
	return pdbq.New(cfg, pdbq.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func buildSDL(tb testing.TB, n int, hub, indexedOnly, aggregates, simple bool) string {
	tb.Helper()
	built, err := scaleApp(indexedOnly, aggregates, simple).BuildSchema(context.Background(), genCatalog(n, hub))
	if err != nil {
		tb.Fatal(err)
	}
	return built.SDL
}

// TestScaleSDLStable pins the SDL generated for a 300-table catalog so that
// changes to schema-building internals cannot alter the output. Regenerate
// the digests with PDBQ_PRINT_SDL_DIGEST=1.
func TestScaleSDLStable(t *testing.T) {
	cases := []struct {
		name                                 string
		hub, indexedOnly, aggregates, simple bool
		want                                 string
	}{
		{"random-indexed", false, true, false, false, "628ae2d58c681882713c2e0c2e78c2ee575e58e110163a611eb3a5c1887aa452"},
		{"random-all", false, false, false, false, "6eed09686f321b59d68f4a5c2230bc0802d276d38ffbf5fa631ee4f2e3cff022"},
		{"hub-all", true, false, false, false, "80155d131be816ea143c7cb9bf0f2815b9cf77220df2f977af362b8b4ba30722"},
		{"hub-aggregates", true, true, true, false, "2dfc85d249c8e004aa44ed4f63872504e33507e40299e7bf6e8f2a921555fabe"},
		{"hub-simple-names", true, false, false, true, "ec9dc749fc965cb4a12da7bb0732d09546408513a2b51c22fad19cd1d233bd1f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdl := buildSDL(t, 300, tc.hub, tc.indexedOnly, tc.aggregates, tc.simple)
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(sdl)))
			if os.Getenv("PDBQ_PRINT_SDL_DIGEST") != "" {
				t.Logf("%s %s len=%d", tc.name, got, len(sdl))
				return
			}
			if got != tc.want {
				t.Errorf("SDL digest = %s, want %s", got, tc.want)
			}
		})
	}
}

func BenchmarkSchemaBuildLarge(b *testing.B) {
	for _, n := range []int{1000, 3000} {
		for _, hub := range []bool{false, true} {
			b.Run(fmt.Sprintf("tables=%d/hub=%t", n, hub), func(b *testing.B) {
				benchBuild(b, n, hub, false)
			})
		}
	}
}

func BenchmarkSchemaBuildLargeAggregates(b *testing.B) {
	for _, n := range []int{100, 200} {
		b.Run(fmt.Sprintf("tables=%d/hub=true", n), func(b *testing.B) {
			benchBuild(b, n, true, true)
		})
	}
}

func benchBuild(b *testing.B, n int, hub, aggregates bool) {
	cat := genCatalog(n, hub)
	app := scaleApp(false, aggregates, false)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := app.BuildSchema(context.Background(), cat); err != nil {
			b.Fatal(err)
		}
	}
}
