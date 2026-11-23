package schema_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/schema"
)

func TestDefaultSchema(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type User implements Node ", "type Post implements Node ",
		"interface Node ", "nodeId: ID!", "node(nodeId: ID!): Node",
		"allUsers(", "userById(", "userByEmail(",
		"createUser(", "updateUserById(", "deleteUserById(",
		"input UserFilter ", "enum UsersOrderBy ", "enum Mood ",
		"postsByAuthorId(", "author: User",
		"searchPosts(", "scalar BigInt", "type PageInfo ",
		"): UserConnection!", "): PostConnection!",
		"first: Int, last: Int, offset: Int, before: Cursor, after: Cursor",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	if strings.Contains(built.SDL, "allUsersConnection(") {
		t.Error("separate connection field should be gone")
	}
	// Indexed-only filter policy: posts.published (unindexed) not filterable.
	if strings.Contains(sdlType(built.SDL, "input PostFilter"), "published") {
		t.Error("unindexed column 'published' leaked into PostFilter")
	}
	if built.Meta["Query"]["allUsers"] == nil || built.Meta["Query"]["allUsers"].Kind != schema.KindConnectionQuery {
		t.Error("missing connection query metadata on allUsers")
	}
	if built.Meta["Query"]["node"] == nil || built.Meta["Query"]["node"].Kind != schema.KindNode {
		t.Error("missing node query metadata")
	}
	if built.Meta["User"]["nodeId"] == nil || built.Meta["User"]["nodeId"].Kind != schema.KindNodeID {
		t.Error("missing nodeId metadata on User")
	}
	if built.TableForType["User"] == nil || built.TableForType["User"].Name != "users" {
		t.Error("TableForType missing User -> users")
	}
}

// TestTableBackedEnum covers the @enum table conversion surface: the
// referencing column is typed as the generated enum everywhere (output,
// filter, create/update inputs), value descriptions render, and no table
// surface exists for the enum table itself.
func TestTableBackedEnum(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"enum EventType ", "CONFERENCE", "HACKATHON", "MEETUP",
		`"""A large formal gathering"""`,
		"typeCode: EventType!",         // output field + create input
		"typeCode: EventTypeFilterOps", // filter
		"TYPE_CODE_ASC",                // orderBy
		"allEvents(", "createEvent(",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	for _, reject := range []string{"allEventTypes(", "type EventType "} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL must not contain %q", reject)
		}
	}
	if built.EnumValues["EventType"]["MEETUP"] != "meetup" {
		t.Errorf("EnumValues[EventType][MEETUP] = %q, want meetup", built.EnumValues["EventType"]["MEETUP"])
	}
	if built.EnumTypeForPG["public.event_type"] != "EventType" {
		t.Errorf("EnumTypeForPG missing table-backed enum: %v", built.EnumTypeForPG["public.event_type"])
	}
}

func TestComputedColumns(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions,
		// Volatile row-type functions are never computed columns.
		&introspect.Function{
			Schema: "public", Name: "users_touch",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		},
		// Row-type arg in non-first position: not representable, skipped.
		&introspect.Function{
			Schema: "public", Name: "tag_user",
			Args: []introspect.FuncArg{
				{Name: "tag", PGType: "text", TypeSchema: "pg_catalog"},
				{Name: "u", PGType: "users", TypeSchema: "public"},
			},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		},
		// No table-name prefix: full function name becomes the field name.
		&introspect.Function{
			Schema: "public", Name: "word_count",
			Args:       []introspect.FuncArg{{Name: "p", PGType: "posts", TypeSchema: "public"}},
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityImmutable,
		},
	)
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	userType := sdlType(built.SDL, "type User implements Node")
	if !strings.Contains(userType, "postCount: BigInt") {
		t.Errorf("User missing computed postCount:\n%s", userType)
	}
	postType := sdlType(built.SDL, "type Post implements Node")
	if !strings.Contains(postType, "excerpt(maxChars: Int): String") {
		t.Errorf("Post missing computed excerpt(maxChars:):\n%s", postType)
	}
	if !strings.Contains(postType, "wordCount: Int") {
		t.Errorf("Post missing unprefixed computed wordCount:\n%s", postType)
	}
	if meta := built.Meta["User"]["postCount"]; meta == nil || meta.Kind != schema.KindComputed || meta.Function == nil {
		t.Error("missing KindComputed metadata on User.postCount")
	}
	for _, reject := range []string{"usersPostCount", "postsExcerpt", "usersTouch", "touch(", "tagUser"} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL should not contain %q", reject)
		}
	}
}

func TestFilterAllColumnsWhenPolicyOff(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: false})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sdlType(built.SDL, "input PostFilter"), "published") {
		t.Error("published should be filterable with indexed_only: false")
	}
}

func TestFunctionMutationShape(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	// Volatile functions get the Relay-classic mutation shape.
	for _, want := range []string{
		"addNumbers(input: AddNumbersInput!): AddNumbersPayload!",
		"publishPost(input: PublishPostInput!): PublishPostPayload!",
		"retryProbe(input: RetryProbeInput!): RetryProbePayload!",
		"input AddNumbersInput ", "type AddNumbersPayload ",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	in := sdlType(built.SDL, "input AddNumbersInput")
	for _, want := range []string{"a: Int", "b: Int", "clientMutationId: String"} {
		if !strings.Contains(in, want) {
			t.Errorf("AddNumbersInput missing %q:\n%s", want, in)
		}
	}
	pay := sdlType(built.SDL, "type AddNumbersPayload")
	for _, want := range []string{"result: Int", "clientMutationId: String"} {
		if !strings.Contains(pay, want) {
			t.Errorf("AddNumbersPayload missing %q:\n%s", want, pay)
		}
	}
	// Table-returning volatile function: result is the table's object type.
	if !strings.Contains(sdlType(built.SDL, "type PublishPostPayload"), "result: Post") {
		t.Error("PublishPostPayload result should be Post")
	}
	// Stable functions keep flat args on Query.
	if !strings.Contains(built.SDL, "searchPosts(term: String): [Post!]!") {
		t.Error("stable function should keep flat args")
	}
	if m := built.Meta["Mutation"]["addNumbers"]; m == nil || m.Kind != schema.KindFunction {
		t.Error("missing KindFunction metadata on Mutation.addNumbers")
	}
	if m := built.Meta["AddNumbersPayload"]["result"]; m == nil || m.Kind != schema.KindPayloadResult {
		t.Error("missing KindPayloadResult metadata on AddNumbersPayload.result")
	}
}

func TestSimpleNamesPlugin(t *testing.T) {
	reg := plugin.NewRegistry(simplenames.New())
	cat := testutil.FixtureCatalog()
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	b := schema.New(cat, reg.Inflector(nil), schema.Options{FilterIndexedOnly: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"users(", "user(", "updateUser(", "deleteUser(", "posts(",
	} {
		if !strings.Contains(built.SDL, "  "+want) {
			t.Errorf("simple-names SDL missing %q", want)
		}
	}
	for _, reject := range []string{"allUsers(", "userById(", "postsByAuthorId("} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("simple-names SDL still contains verbose %q", reject)
		}
	}
}

func TestNestedMutationsSchema(t *testing.T) {
	reg := plugin.NewRegistry(nestedmutations.New(nil))
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	postCreate := sdlType(built.SDL, "input PostCreateInput")
	if !strings.Contains(postCreate, "author: PostAuthorNestedInput") {
		t.Errorf("PostCreateInput missing nested author field:\n%s", postCreate)
	}
	// FK column must have become optional (nested input can supply it).
	if strings.Contains(postCreate, "authorId: Int!") {
		t.Error("authorId should be optional once nested input exists")
	}
	userCreate := sdlType(built.SDL, "input UserCreateInput")
	if !strings.Contains(userCreate, "postsByAuthorId: UserPostsByAuthorIdNestedInput") {
		t.Errorf("UserCreateInput missing nested children field:\n%s", userCreate)
	}
	if !strings.Contains(built.SDL, "input UserConnectByIdInput") {
		t.Error("missing UserConnectByIdInput")
	}
}

// sdlType extracts one type block from SDL for focused assertions.
func sdlType(sdl, header string) string {
	i := strings.Index(sdl, header)
	if i < 0 {
		return ""
	}
	j := strings.Index(sdl[i:], "}")
	if j < 0 {
		return sdl[i:]
	}
	return sdl[i : i+j+1]
}

func TestPartitionsHiddenByDefault(t *testing.T) {
	partitioned := func() *introspect.Catalog {
		cat := testutil.FixtureCatalog()
		cols := func() []*introspect.Column {
			return []*introspect.Column{
				{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
				{Name: "at", Position: 2, PGType: "date", TypeSchema: "pg_catalog", NotNull: true},
			}
		}
		privs := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
		cat.Tables = append(cat.Tables,
			&introspect.Table{
				Schema: "public", Name: "measurements", Kind: introspect.RelTable, Partitioned: true,
				Columns: cols(), Privileges: privs,
				PrimaryKey: &introspect.Constraint{Name: "measurements_pkey", Columns: []string{"id", "at"}},
			},
			&introspect.Table{
				Schema: "public", Name: "measurements_2026", Kind: introspect.RelTable,
				PartitionOf: "public.measurements", Columns: cols(), Privileges: privs,
			})
		cat.Functions = append(cat.Functions, &introspect.Function{
			Schema: "public", Name: "latest_2026",
			ReturnType: "measurements_2026", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		})
		return cat
	}
	build := func(expose bool) string {
		b := schema.New(partitioned(), inflect.Default, schema.Options{Functions: true, ExposePartitions: expose})
		built, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		return built.SDL
	}

	sdl := build(false)
	if !strings.Contains(sdl, "allMeasurements(") {
		t.Error("partitioned parent missing")
	}
	for _, leaked := range []string{"Measurements2026", "measurements2026", "latest2026"} {
		if strings.Contains(sdl, leaked) {
			t.Errorf("partition surface %q exposed by default", leaked)
		}
	}
	sdl = build(true)
	for _, want := range []string{"allMeasurements2026s(", "latest2026: [Measurements2026!]!"} {
		if !strings.Contains(sdl, want) {
			t.Errorf("expose_partitions: SDL missing %q", want)
		}
	}
}

func relnameCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memos", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "user_email", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "memos_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "memos_user_email_fkey", Columns: []string{"user_email"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"email"},
		}},
	})
	return cat
}

func TestForwardRelationNameCollidesWithColumn(t *testing.T) {
	check := func(t *testing.T, b *schema.Builder) {
		t.Helper()
		built, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		memo := sdlType(built.SDL, "type Memo ")
		for _, want := range []string{"userEmail: String!", "userByUserEmail: User"} {
			if !strings.Contains(memo, want) {
				t.Errorf("Memo missing %q:\n%s", want, memo)
			}
		}
		if f := built.Meta["Memo"]["userByUserEmail"]; f == nil || f.Kind != schema.KindRelationForward {
			t.Error("missing forward relation metadata on Memo.userByUserEmail")
		}
	}
	t.Run("default", func(t *testing.T) {
		check(t, schema.New(relnameCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}))
	})
	t.Run("simple-names", func(t *testing.T) {
		reg := plugin.NewRegistry(simplenames.New())
		cat := relnameCatalog()
		if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
			t.Fatal(err)
		}
		check(t, schema.New(cat, reg.Inflector(nil), schema.Options{FilterIndexedOnly: true}))
	})
}

func TestSubscriptionsSchema(t *testing.T) {
	build := func(subs bool) *schema.Built {
		t.Helper()
		b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
			FilterIndexedOnly: true,
			Functions:         true,
			Subscriptions:     subs,
		})
		built, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	off, on := build(false), build(true)
	if off.Schema.Subscription != nil || strings.Contains(off.SDL, "Subscription") || strings.Contains(off.SDL, "ListenPayload") {
		t.Fatal("schema without subscriptions declares subscription types")
	}
	if on.Schema.Subscription == nil || on.Schema.Subscription.Name != "Subscription" {
		t.Fatal("Subscription root missing")
	}
	sub := sdlType(on.SDL, "type Subscription")
	payload := sdlType(on.SDL, "type ListenPayload")
	for _, want := range []string{"listen(topic: String!, ", "initialEvent: Boolean! = false): ListenPayload!"} {
		if !strings.Contains(sub, want) {
			t.Errorf("Subscription missing %q:\n%s", want, sub)
		}
	}
	for _, want := range []string{"query: Query\n", "relatedNode: Node\n", "relatedNodeId: ID\n"} {
		if !strings.Contains(payload, want) {
			t.Errorf("ListenPayload missing %q:\n%s", want, payload)
		}
	}
	if m := on.Meta["Subscription"]["listen"]; m == nil || m.Kind != schema.KindListen {
		t.Errorf("listen meta = %+v", m)
	}
	if m := on.Meta["ListenPayload"]["relatedNode"]; m == nil || m.Kind != schema.KindListenRelatedNode {
		t.Errorf("relatedNode meta = %+v", m)
	}
	stripped := strings.Replace(on.SDL, sub+"\n\n", "", 1)
	stripped = strings.Replace(stripped, `"""The payload of a listen subscription event."""`+"\n"+payload+"\n\n", "", 1)
	if stripped != off.SDL {
		t.Error("enabling subscriptions changed the rest of the schema")
	}
}
