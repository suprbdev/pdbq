package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

const headlineBoundSchema = "headline_bound_e2e"

// TestHeadlineDocumentBound reproduces many headline aliases over large
// documents on a page of 50 passing the cost check at a flat price per
// headline, and checks headlines are priced by the document bound and read
// only server.max_headline_document characters.
func TestHeadlineDocumentBound(t *testing.T) {
	conn := testConn(t)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+headlineBoundSchema+` CASCADE`)
	})
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+headlineBoundSchema+` CASCADE`,
		`CREATE SCHEMA `+headlineBoundSchema,
		`CREATE TABLE `+headlineBoundSchema+`.big (id int PRIMARY KEY, body text,
			search tsvector GENERATED ALWAYS AS (to_tsvector('simple', left(body, 900000))) STORED)`,
		`CREATE INDEX ON `+headlineBoundSchema+`.big USING gin (search)`,
		`INSERT INTO `+headlineBoundSchema+`.big (id, body) SELECT g, repeat('lorem ipsum dolor sit amet fox ', 3000) FROM generate_series(1, 20) g`,
		`INSERT INTO `+headlineBoundSchema+`.big (id, body) VALUES (100, repeat('lorem ', 20) || 'fox')`,
	)
	serve := func(maxDoc int) string {
		return testServer(t, func(c *config.Config) {
			c.Schema.Schemas = []string{headlineBoundSchema}
			if maxDoc > 0 {
				c.Server.MaxHeadlineDocument = maxDoc
			}
		}).URL
	}
	url := serve(0)

	t.Run("many aliases exceed the cost limit", func(t *testing.T) {
		var q strings.Builder
		q.WriteString(`{ allBigs(first: 50, filter: {search: {matches: "fox"}}) { nodes { id`)
		for i := range 9 {
			q.WriteString(` h` + string(rune('1'+i)) + `: searchHeadline(document: BODY)`)
		}
		q.WriteString(` } } }`)
		res := post(t, url, q.String(), nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "cost") {
			t.Fatalf("want a cost error, got %+v %s", res.Errors, res.Data)
		}
	})

	headline := func(t *testing.T, url string) string {
		t.Helper()
		res := post(t, url, `{ allBigs(filter: {search: {matches: "fox"}, id: {equalTo: 100}}) { nodes { searchHeadline(document: BODY) } } }`, nil)
		requireNoErrors(t, res)
		return string(res.Data["allBigs"])
	}
	t.Run("default bound highlights a match", func(t *testing.T) {
		if got := headline(t, url); !strings.Contains(got, "<b>fox</b>") {
			t.Errorf("headline = %s, want fox highlighted", got)
		}
		res := post(t, url, `{ allBigs(first: 20, filter: {search: {matches: "fox"}}) { nodes { searchHeadline(document: BODY) } } }`, nil)
		requireNoErrors(t, res)
	})
	t.Run("a match past the bound is not read", func(t *testing.T) {
		got := headline(t, serve(40))
		if strings.Contains(got, "<b>") || !strings.Contains(got, "lorem") {
			t.Errorf("headline = %s, want the opening words without a match", got)
		}
	})
}
