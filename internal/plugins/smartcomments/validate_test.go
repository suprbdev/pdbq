package smartcomments_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
)

// buildLogged builds the fixture schema with the smart_comments plugin,
// returning what the build logged.
func buildLogged(t *testing.T, prep func(*introspect.Catalog)) string {
	t.Helper()
	cat := testutil.FixtureCatalog()
	prep(cat)
	reg := plugin.NewRegistry(smartcomments.New())
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	b := schema.New(cat, reg.Inflector(nil), schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
		Logger:            slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	return logs.String()
}

func warnings(logs, msg string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, msg) {
			out = append(out, line)
		}
	}
	return out
}

func TestUnknownSmartTagWarnings(t *testing.T) {
	logs := buildLogged(t, func(c *introspect.Catalog) {
		users := c.Table("public", "users")
		users.Comment = "@omit reed, delete\n@omit reed\n@behavior -filterBy +aggregates -contract:partner -selectt"
		users.Column("email").Comment = "@omit filter\n@behavior -orderBy -groupBy -aggregate -orderby -Bogus"
		c.Table("public", "posts").ForeignKeys[0].Comment = "@omit many,fliter"
		for _, f := range c.Functions {
			if f.Name == "users_post_count" {
				f.Comment = "@behavior -filterby contract:public +nothing"
			}
		}
	})
	omits := warnings(logs, "unknown @omit action")
	if len(omits) != 2 ||
		!strings.Contains(omits[0], "constraint=public.posts.") || !strings.Contains(omits[0], "action=fliter") ||
		!strings.Contains(omits[1], "table=public.users") || !strings.Contains(omits[1], "action=reed") {
		t.Errorf("omit warnings = %q", omits)
	}
	behaviors := warnings(logs, "unknown @behavior token")
	want := []string{"table=public.users token=-selectt", "column=public.users.email token=-Bogus", "function=public.users_post_count token=+nothing"}
	if len(behaviors) != len(want) {
		t.Fatalf("behavior warnings = %q, want %q", behaviors, want)
	}
	for i, w := range want {
		if !strings.Contains(behaviors[i], w) {
			t.Errorf("behavior warning %d = %q, want %q", i, behaviors[i], w)
		}
	}
}

func TestDeprecatedBehaviorWarnings(t *testing.T) {
	logs := buildLogged(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Column("email").Comment = "@behavior -filter -order -filter"
	})
	got := warnings(logs, "deprecated @behavior token")
	if len(got) != 2 ||
		!strings.Contains(got[0], "column=public.users.email token=-filter use=-filterBy") ||
		!strings.Contains(got[1], "column=public.users.email token=-order use=-orderBy") {
		t.Errorf("deprecation warnings = %q", got)
	}
	if strings.Contains(logs, "unknown @behavior token") {
		t.Errorf("deprecated tokens reported as unknown: %s", logs)
	}
}

func TestKnownSmartTagsDoNotWarn(t *testing.T) {
	logs := buildLogged(t, func(c *introspect.Catalog) {
		users := c.Table("public", "users")
		users.Comment = "@omit read, ALL, many, create, update, delete, filter, order, aggregates\n" +
			"@behavior -select -insert -update -delete -filterBy -orderBy +aggregates -groupedAggregates " +
			"-aggregates:filterBy -aggregates:orderBy +contract:public -contract:partner contract:internal"
		users.Column("email").Comment = "@behavior -aggregate -groupBy -attribute:groupBy -attribute:aggregate"
	})
	if w := warnings(logs, "@omit action"); len(w) > 0 {
		t.Errorf("omit warnings: %q", w)
	}
	if w := warnings(logs, "@behavior token"); len(w) > 0 {
		t.Errorf("behavior warnings: %q", w)
	}
}
