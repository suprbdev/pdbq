package compile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/schema"
)

// temporalTables are PostgreSQL 18 temporal objects the shared pdbcore
// fixture lacks: room_bookings has a WITHOUT OVERLAPS primary key,
// booking_notes a PERIOD foreign key to it and a NOT ENFORCED foreign key to
// users. test/e2e_test.go creates the same tables on PostgreSQL 18+.
func temporalTables() []*introspect.Table {
	col := func(pos int, name, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: "pg_catalog", NotNull: true}
	}
	all := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	bookings := &introspect.Table{
		Schema: "public", Name: "room_bookings", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			col(1, "room_id", "int4"), col(2, "valid_at", "daterange"), col(3, "guest", "text"),
		},
		PrimaryKey: &introspect.Constraint{Name: "room_bookings_pkey", Columns: []string{"room_id", "valid_at"}, WithoutOverlaps: true},
		Indexes: []*introspect.Index{
			{Name: "room_bookings_pkey", Columns: []string{"room_id", "valid_at"}, Method: "gist"},
		},
	}
	id := col(1, "id", "int4")
	id.Generated, id.GeneratedKind = true, introspect.GeneratedIdentity
	notes := &introspect.Table{
		Schema: "public", Name: "booking_notes", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			id, col(2, "room_id", "int4"), col(3, "valid_at", "daterange"), col(4, "note", "text"), col(5, "author_id", "int4"),
		},
		PrimaryKey: &introspect.Constraint{Name: "booking_notes_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "booking_notes_room_id_valid_at_fkey", Columns: []string{"room_id", "valid_at"},
			RefSchema: "public", RefTable: "room_bookings", RefColumns: []string{"room_id", "valid_at"},
			Period: true,
		}, {
			Name: "booking_notes_author_id_fkey", Columns: []string{"author_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
			NotEnforced: true,
		}},
		Indexes: []*introspect.Index{
			{Name: "booking_notes_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "booking_notes_room_idx", Columns: []string{"room_id", "valid_at"}, Method: "gist"},
			{Name: "booking_notes_author_idx", Columns: []string{"author_id"}, Method: "btree"},
		},
	}
	return []*introspect.Table{bookings, notes}
}

// buildTemporal is build plus the relation plugins (advanced-filters,
// nested-mutations) whose surface follows temporal foreign keys; it returns
// the compile chain to run documents through.
func buildTemporal(t testing.TB) (*schema.Built, *plugin.Registry) {
	t.Helper()
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, temporalTables()...)
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	reg := plugin.NewRegistry(advancedfilters.New(nil), nestedmutations.New(nil))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built, reg
}

func TestTemporalSchema(t *testing.T) {
	built, _ := buildTemporal(t)
	sdl := built.SDL
	for _, want := range []string{
		"roomBookingByRoomId(roomId: Int!, \"\"\"",
		"at: Date!",
		"roomBookingsByRoomIdAndValidAt(first: Int, last: Int, offset: Int, before: Cursor, after: Cursor",
		"): RoomBookingConnection!",
		"bookingNotesByRoomIdAndValidAt(",
		"author: User\n",
		"roomBookingsByRoomIdAndValidAt: RoomBookingToManyFilter",
		"roomBookingsByRoomIdAndValidAtExist: Boolean",
		"bookingNotesByRoomIdAndValidAt: BookingNoteToManyFilter",
		"ROOM_BOOKINGS_BY_ROOM_ID_AND_VALID_AT__ROOM_ID_ASC",
		"updateRoomBookingByRoomIdAndValidAt(", "deleteRoomBookingByRoomIdAndValidAt(",
		"type RoomBooking implements Node",
		"input RoomBookingConnectInput {\n  roomId: Int!\n}",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"roomBookingByRoomIdAndValidAt(",
		"upsertRoomBooking",
		"roomBookingsByRoomIdAndValidAt: RoomBookingFilter",
	} {
		if strings.Contains(sdl, unwanted) {
			t.Errorf("SDL has %q", unwanted)
		}
	}
	in := sdlBlock(sdl, "input BookingNoteCreateInput {")
	if !strings.Contains(in, "roomId: Int\n") || !strings.Contains(in, "validAt: String!\n") {
		t.Errorf("nested PERIOD FK: room id should be optional, the period required:\n%s", in)
	}
	lookup := built.Meta["Query"]["roomBookingByRoomId"]
	if lookup == nil || lookup.PeriodColumn != "valid_at" || strings.Join(lookup.KeyColumns, ",") != "room_id" {
		t.Errorf("point-in-time lookup meta = %+v", lookup)
	}
}

// sdlBlock returns the SDL type definition starting at header.
func sdlBlock(sdl, header string) string {
	i := strings.Index(sdl, header)
	if i < 0 {
		return ""
	}
	rest := sdl[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j+2]
	}
	return rest
}
