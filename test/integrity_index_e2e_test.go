package e2e

import (
	"context"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

const integritySchema = "integ_e2e"

// TestIntegrityErrorsHideIndexNames reproduces unique violations of an
// expression index and a partial index over an @omit column passing the
// index name through with errors.detail: prod, and checks they read as a
// generic unique violation while constraints over visible columns keep
// PostgreSQL's message.
func TestIntegrityErrorsHideIndexNames(t *testing.T) {
	conn := testConn(t)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+integritySchema+` CASCADE`) })
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+integritySchema+` CASCADE`,
		`CREATE SCHEMA `+integritySchema,
		`SET search_path TO `+integritySchema,
		`CREATE TABLE ux (id int PRIMARY KEY, owner int, secret text, code text, note text)`,
		`COMMENT ON COLUMN ux.secret IS '@omit'`,
		`CREATE UNIQUE INDEX ux_owner_secret_lower_key ON ux (owner, lower(secret))`,
		`CREATE UNIQUE INDEX ux_code_secret_partial_key ON ux (code) WHERE secret LIKE 'vip%'`,
		`CREATE UNIQUE INDEX ux_note_lower_key ON ux (lower(note))`,
		`ALTER TABLE ux ADD CONSTRAINT ux_note_len CHECK (length(note) < 10)`,
		`INSERT INTO ux VALUES (1, 1, 'Alpha', 'a', 'n1'), (2, 2, 'alpha', 'a2', 'n2'), (3, 3, 'vipx', 'z', 'n3'), (4, 4, 'vipy', 'y', 'n4')`,
		`RESET search_path`,
	)
	url := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{integritySchema}
		c.Errors.Detail = "prod"
	}).URL
	for _, tc := range []struct{ name, patch, want string }{
		{"expression index over a hidden column", `id: 2, uxPatch: {owner: 1}`,
			`{"data":{"updateUxById":null},"errors":[{"message":"unique violation","path":["updateUxById"],"extensions":{"code":"23505"}}]}`},
		{"partial index predicate on a hidden column", `id: 4, uxPatch: {code: "z"}`,
			`{"data":{"updateUxById":null},"errors":[{"message":"unique violation","path":["updateUxById"],"extensions":{"code":"23505"}}]}`},
		{"expression index over visible columns", `id: 2, uxPatch: {note: "N1"}`,
			`{"data":{"updateUxById":null},"errors":[{"message":"duplicate key value violates unique constraint \"ux_note_lower_key\"","path":["updateUxById"],"extensions":{"code":"23505"}}]}`},
		{"check over visible columns", `id: 1, uxPatch: {note: "0123456789abc"}`,
			`{"data":{"updateUxById":null},"errors":[{"message":"new row for relation \"ux\" violates check constraint \"ux_note_len\"","path":["updateUxById"],"extensions":{"code":"23514"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := postRaw(t, url, `mutation { updateUxById(input: {`+tc.patch+`}) { clientMutationId } }`, nil); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
