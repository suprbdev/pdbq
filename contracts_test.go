package pdbq_test

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq"
)

// contractCatalog is the shared fixture tagged for three contracts:
// internal (everything), public (no email, no comments, no post counts)
// and partner (default exclude: posts and search_posts only). The
// authenticate(user_email) argument is not the users.email column.
func contractCatalog(t *testing.T) *introspect.Catalog {
	t.Helper()
	cat := testutil.FixtureCatalog()
	users, posts, comments := cat.Table("public", "users"), cat.Table("public", "posts"), cat.Table("public", "comments")
	if users == nil || posts == nil || comments == nil {
		t.Fatal("fixture lacks users, posts or comments")
	}
	users.Column("email").Comment = "@behavior -contract:public"
	comments.Comment = "@contract internal"
	posts.Comment = "@behavior +contract:partner"
	for _, f := range cat.Functions {
		switch f.Name {
		case "users_post_count":
			f.Comment = "@behavior -contract:public"
		case "search_posts":
			f.Comment = "@behavior +contract:partner"
		}
	}
	return cat
}

func contractApp(t *testing.T) *pdbq.App {
	t.Helper()
	cfg := pdbq.DefaultConfig()
	cfg.Plugins.Disabled = []string{"simple_names"}
	cfg.Contracts = []pdbq.ContractConfig{{Name: "internal", Path: "/"}, {Name: "public"}, {Name: "partner", Default: "exclude"}}
	return pdbq.New(cfg, pdbq.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func hasType(sdl, name string) bool {
	return regexp.MustCompile(`(?m)^type ` + name + `\b`).MatchString(sdl)
}

func TestContractSchemas(t *testing.T) {
	ctx := context.Background()
	app := contractApp(t)
	cat := contractCatalog(t)
	sdl := map[string]string{}
	for _, name := range []string{"public", "partner", "internal", "public"} {
		built, err := app.BuildContractSchema(ctx, cat, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if prev, ok := sdl[name]; ok && prev != built.SDL {
			t.Fatalf("%s: rebuilding from the same catalog changed the schema", name)
		}
		sdl[name] = built.SDL
	}

	internal := sdl["internal"]
	for _, want := range []string{"email: String!", "userByEmail(", "EMAIL_ASC", "postCount", "allComments("} {
		if !strings.Contains(internal, want) {
			t.Errorf("internal schema lacks %q", want)
		}
	}

	public := sdl["public"]
	for _, l := range strings.Split(public, "\n") {
		if l = strings.ToLower(strings.TrimSpace(l)); strings.Contains(l, "email") && !strings.HasPrefix(l, "useremail:") {
			t.Errorf("public schema mentions the hidden email column: %s", l)
		}
	}
	if hasType(public, "Comment") || strings.Contains(public, "allComments") || strings.Contains(public, "commentsByPostId") {
		t.Error("public schema exposes the internal comments table")
	}
	if strings.Contains(public, "postCount") {
		t.Error("public schema exposes users_post_count")
	}
	if !hasType(public, "User") || !hasType(public, "Post") || !strings.Contains(public, "searchPosts") {
		t.Error("public schema lost untagged surface")
	}

	partner := sdl["partner"]
	if !hasType(partner, "Post") || !strings.Contains(partner, "searchPosts") {
		t.Error("partner schema lacks the tagged-in posts and search_posts")
	}
	if hasType(partner, "User") || hasType(partner, "Comment") || strings.Contains(partner, "userByAuthorId") {
		t.Error("partner schema exposes untagged tables or relations to them")
	}
	if strings.Contains(partner, "addNumbers") {
		t.Error("partner schema exposes an untagged function")
	}
}

func TestContractFullSchemaUnchanged(t *testing.T) {
	ctx := context.Background()
	plain := pdbq.DefaultConfig()
	plain.Plugins.Disabled = []string{"simple_names"}
	want, err := pdbq.New(plain).BuildSchema(ctx, contractCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := contractApp(t).BuildContractSchema(ctx, contractCatalog(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.SDL != want.SDL {
		t.Error("the full schema of an App with contracts differs from one without")
	}
	if _, err := contractApp(t).BuildContractSchema(ctx, contractCatalog(t), "nope"); err == nil {
		t.Error("unknown contract accepted")
	}
}
