package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestNotValidNotNull covers a PostgreSQL 18 NOT NULL ... NOT VALID
// constraint: the column keeps rows holding NULL, so its field stays
// nullable and reads them back, while new NULLs are still rejected.
func TestNotValidNotNull(t *testing.T) {
	conn := testConn(t)
	var version int
	if err := conn.QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 180000 {
		t.Skipf("NOT VALID NOT NULL constraints need PostgreSQL 18+, server_version_num is %d", version)
	}
	execAll(t, conn,
		`CREATE TABLE legacy_contacts (id integer PRIMARY KEY, phone text)`,
		`INSERT INTO legacy_contacts VALUES (1, NULL), (2, '555-0100')`,
		`ALTER TABLE legacy_contacts ADD CONSTRAINT legacy_contacts_phone_not_null NOT NULL phone NOT VALID`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE legacy_contacts`)
	})

	hs := testServer(t, func(c *config.Config) {
		c.Filters.IndexedOnly = false
	})

	res := post(t, hs.URL, `{ __type(name: "LegacyContact") { fields { name type { kind } } } }`, nil)
	requireNoErrors(t, res)
	var typ struct {
		Fields []struct {
			Name string `json:"name"`
			Type struct {
				Kind string `json:"kind"`
			} `json:"type"`
		} `json:"fields"`
	}
	mustUnmarshal(t, res.Data["__type"], &typ)
	found := false
	for _, f := range typ.Fields {
		if f.Name == "phone" {
			found = true
			if f.Type.Kind == "NON_NULL" {
				t.Error("phone is NON_NULL although rows predating its NOT VALID constraint hold NULL")
			}
		}
	}
	if !found {
		t.Fatal("LegacyContact.phone missing")
	}

	res = post(t, hs.URL, `{ allLegacyContacts(orderBy: ID_ASC) { nodes { id phone } } }`, nil)
	requireNoErrors(t, res)
	b, _ := json.Marshal(res.Data)
	if got, want := normalize(t, b), `{"allLegacyContacts":{"nodes":[{"id":1,"phone":null},{"id":2,"phone":"555-0100"}]}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	res = post(t, hs.URL, `mutation { createLegacyContact(input: {legacyContact: {id: 3}}) { legacyContact { id } } }`, nil)
	if len(res.Errors) == 0 {
		t.Error("inserting a NULL phone succeeded despite the NOT NULL constraint")
	}
}
