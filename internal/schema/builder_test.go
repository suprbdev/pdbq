package schema_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	corecatalog "github.com/suprbdev/pdbcore/smartcomments/catalog"
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
		// Volatile row-type functions are custom mutations, not computed columns.
		&introspect.Function{
			Schema: "public", Name: "users_touch",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		},
		// Row-type arg in non-first position: a root query taking UserInput.
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
	for _, reject := range []string{"usersPostCount", "postsExcerpt", "touch(", "  touch"} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL should not contain %q", reject)
		}
	}
	if q := sdlType(built.SDL, "type Query"); !strings.Contains(q, "tagUser(tag: String, u: UserInput): String") {
		t.Errorf("Query missing tagUser(tag:, u: UserInput):\n%s", q)
	}
	if m := sdlType(built.SDL, "type Mutation"); !strings.Contains(m, "usersTouch(input: UsersTouchInput!): UsersTouchPayload!") {
		t.Errorf("Mutation missing usersTouch:\n%s", m)
	}
	if in := sdlType(built.SDL, "input UserInput"); !strings.Contains(in, "  id: Int\n") || !strings.Contains(in, "  prevAddresses: [AddressInput!]\n") || strings.Contains(in, "!\n  email") || strings.Contains(in, "clientMutationId") {
		t.Errorf("UserInput must hold every column, all nullable:\n%s", in)
	}
	if built.RowInputForPG["public.users"] != "UserInput" {
		t.Errorf("RowInputForPG = %v", built.RowInputForPG)
	}
}

func TestPolymorphicFunctionsNeedConcreteTypes(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{Functions: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, reject := range []string{"pickFirst", "largerOf", "rangeLower", "arrayFirst"} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("unresolved polymorphic function exposed: %q", reject)
		}
	}
	cat := testutil.FixtureCatalog()
	if err := corecatalog.New().TransformCatalog(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	built, err = schema.New(cat, inflect.Default, schema.Options{Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	q := sdlType(built.SDL, "type Query")
	for _, want := range []string{
		"pickFirst(a: String, b: String): String",
		"largerOf(a: BigFloat, b: BigFloat): BigFloat",
		"rangeLower(r: IntRangeInput): Int",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("Query missing %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "arrayFirst") {
		t.Error("untagged polymorphic function exposed")
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
	// Volatile functions get the PostGraphile mutation shape.
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
	for _, want := range []string{"integer: Int", "clientMutationId: String", "query: Query"} {
		if !strings.Contains(pay, want) {
			t.Errorf("AddNumbersPayload missing %q:\n%s", want, pay)
		}
	}
	// A table-returning volatile function's payload carries the row: named
	// after its type, with the row's forward relations and edge.
	pay = sdlType(built.SDL, "type PublishPostPayload")
	for _, want := range []string{
		"post: Post", "clientMutationId: String", "query: Query", "author: User",
		"postEdge(orderBy: [PostsOrderBy!]): PostEdge",
	} {
		if !strings.Contains(pay, want) {
			t.Errorf("PublishPostPayload missing %q:\n%s", want, pay)
		}
	}
	// Table mutation payloads get the same fields; bulk ones only query.
	pay = sdlType(built.SDL, "type CreatePostPayload")
	for _, want := range []string{"post: Post", "query: Query", "author: User", "postEdge(orderBy: [PostsOrderBy!]): PostEdge"} {
		if !strings.Contains(pay, want) {
			t.Errorf("CreatePostPayload missing %q:\n%s", want, pay)
		}
	}
	pay = sdlType(built.SDL, "type CreateManyPostPayload")
	if !strings.Contains(pay, "query: Query") || strings.Contains(pay, "postEdge") || strings.Contains(pay, "author") {
		t.Errorf("CreateManyPostPayload should carry query only:\n%s", pay)
	}
	// Stable functions keep flat args on Query.
	if !strings.Contains(built.SDL, "searchPosts(term: String): [Post!]!") {
		t.Error("stable function should keep flat args")
	}
	if m := built.Meta["Mutation"]["addNumbers"]; m == nil || m.Kind != schema.KindFunction {
		t.Error("missing KindFunction metadata on Mutation.addNumbers")
	}
	if m := built.Meta["AddNumbersPayload"]["integer"]; m == nil || m.Kind != schema.KindPayloadResult {
		t.Error("missing KindPayloadResult metadata on AddNumbersPayload.integer")
	}
	if m := built.Meta["PublishPostPayload"]["postEdge"]; m == nil || m.Kind != schema.KindPayloadEdge {
		t.Error("missing KindPayloadEdge metadata on PublishPostPayload.postEdge")
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

// TestLiveSchema checks that live queries repeat every Query field on the
// Subscription root and declare the live directive, and that the option
// leaves the schema byte-for-byte unchanged when it is off or when
// subscriptions are off.
func TestLiveSchema(t *testing.T) {
	build := func(subs, live bool) *schema.Built {
		t.Helper()
		built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
			FilterIndexedOnly: true,
			Functions:         true,
			Subscriptions:     subs,
			Live:              live,
		}).Build()
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	if off, liveOnly := build(false, false), build(false, true); liveOnly.SDL != off.SDL {
		t.Error("live without subscriptions changed the schema")
	}
	subs, on := build(true, false), build(true, true)
	if strings.Contains(subs.SDL, "@live") || subs.Schema.Directives["live"] != nil {
		t.Fatal("schema without live declares the live directive")
	}
	if d := on.Schema.Directives["live"]; d == nil || len(d.Locations) != 1 || d.Locations[0] != "QUERY" || len(d.Arguments) != 0 {
		t.Fatalf("live directive = %+v", on.Schema.Directives["live"])
	}
	query, sub := on.Schema.Query, on.Schema.Subscription
	if sub.Fields.ForName("listen") == nil {
		t.Error("listen missing from the live Subscription root")
	}
	n := 0
	for _, f := range query.Fields {
		if strings.HasPrefix(f.Name, "__") {
			continue
		}
		n++
		sf := sub.Fields.ForName(f.Name)
		if sf == nil {
			t.Errorf("Query.%s not repeated on Subscription", f.Name)
			continue
		}
		if sf.Type.String() != f.Type.String() || len(sf.Arguments) != len(f.Arguments) {
			t.Errorf("Subscription.%s = %s (%d args), want %s (%d args)", f.Name, sf.Type, len(sf.Arguments), f.Type, len(f.Arguments))
		}
		if on.Meta["Subscription"][f.Name] != on.Meta["Query"][f.Name] {
			t.Errorf("Subscription.%s does not share Query.%s's metadata", f.Name, f.Name)
		}
	}
	if n < 5 {
		t.Fatalf("only %d Query fields", n)
	}
	if len(on.Meta["Subscription"]) != n+1 {
		t.Errorf("Subscription has %d fields, want the %d Query fields and listen", len(on.Meta["Subscription"]), n)
	}
	onSub, subsSub := sdlType(on.SDL, "type Subscription"), sdlType(subs.SDL, "type Subscription")
	stripped := strings.Replace(on.SDL, onSub, subsSub, 1)
	stripped = strings.TrimPrefix(stripped, stripped[:strings.Index(stripped, "directive @live on QUERY\n\n")+len("directive @live on QUERY\n\n")])
	if stripped != subs.SDL {
		t.Error("live changed the schema outside the Subscription root and the directive")
	}
}

// TestPayloadRelationFallsBackOnCollision covers a forward relation whose
// name is taken on the payload (here by query): it takes the
// <referencedTable>By<Columns> form there, keeping its name on the row type.
func TestPayloadRelationFallsBackOnCollision(t *testing.T) {
	cat := testutil.FixtureCatalog()
	privs := introspect.Privileges{Select: true, Insert: true}
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "saved_queries", Kind: introspect.RelTable, Privileges: privs,
		Columns:    []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true}},
		PrimaryKey: &introspect.Constraint{Name: "saved_queries_pkey", Columns: []string{"id"}},
	}, &introspect.Table{
		Schema: "public", Name: "runs", Kind: introspect.RelTable, Privileges: privs,
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "query_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "runs_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "runs_query_id_fkey", Columns: []string{"query_id"},
			RefSchema: "public", RefTable: "saved_queries", RefColumns: []string{"id"},
		}},
	})
	built, err := schema.New(cat, inflect.Default, schema.Options{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if run := sdlType(built.SDL, "type Run "); !strings.Contains(run, "query: SavedQuery") {
		t.Errorf("Run should keep the query relation:\n%s", run)
	}
	pay := sdlType(built.SDL, "type CreateRunPayload")
	if !strings.Contains(pay, "query: Query") || !strings.Contains(pay, "savedQueryByQueryId: SavedQuery") {
		t.Errorf("CreateRunPayload should keep query and rename the relation:\n%s", pay)
	}
	if m := built.Meta["CreateRunPayload"]["savedQueryByQueryId"]; m == nil || m.Kind != schema.KindRelationForward {
		t.Error("missing relation metadata on CreateRunPayload.savedQueryByQueryId")
	}
}

// TestIncrementalDeliverySchema checks that the defer and stream directives
// are declared only with the option, and that declaring them changes
// nothing else.
func TestIncrementalDeliverySchema(t *testing.T) {
	build := func(on bool) *schema.Built {
		t.Helper()
		built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
			FilterIndexedOnly:   true,
			Functions:           true,
			IncrementalDelivery: on,
		}).Build()
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	off, on := build(false), build(true)
	if strings.Contains(off.SDL, "directive @") || off.Schema.Directives["stream"] != nil {
		t.Fatal("schema without incremental delivery declares directives")
	}
	if def := build(false).SDL; def != off.SDL {
		t.Fatal("schema SDL is not deterministic")
	}
	end := strings.Index(on.SDL, ") on FIELD\n\n")
	if !strings.HasPrefix(on.SDL, `"""Directs the executor to defer`) || end < 0 {
		t.Fatalf("directives not declared first:\n%.400s", on.SDL)
	}
	if rest := on.SDL[end+len(") on FIELD\n\n"):]; rest != off.SDL {
		t.Error("declaring the directives changed the rest of the schema")
	}
	stream := on.Schema.Directives["stream"]
	if stream == nil || len(stream.Locations) != 1 || stream.Locations[0] != "FIELD" {
		t.Fatalf("stream directive = %+v", stream)
	}
	if a := stream.Arguments.ForName("initialCount"); a == nil || a.Type.String() != "Int!" || a.DefaultValue.Raw != "0" {
		t.Errorf("stream initialCount = %+v", a)
	}
	if a := on.Schema.Directives["defer"].Arguments.ForName("if"); a == nil || a.Type.String() != "Boolean!" {
		t.Errorf("defer if = %+v", a)
	}
}

func TestDeletedNodeIDField(t *testing.T) {
	build := func(cat *introspect.Catalog) string {
		t.Helper()
		b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
		built, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		return built.SDL
	}
	sdl := build(testutil.FixtureCatalog())
	for _, typ := range []string{"type DeleteUserPayload", "type DeletePostPayload"} {
		name := "deletedUserNodeId: ID"
		if strings.Contains(typ, "Post") {
			name = "deletedPostNodeId: ID"
		}
		if body := sdlType(sdl, typ); !strings.Contains(body, name) {
			t.Errorf("%s missing %q:\n%s", typ, name, body)
		}
	}
	for _, typ := range []string{"type CreateUserPayload", "type UpdateUserPayload", "type DeleteUsersPayload"} {
		if body := sdlType(sdl, typ); strings.Contains(body, "deleted") {
			t.Errorf("%s must not carry a deleted nodeId:\n%s", typ, body)
		}
	}

	cat := testutil.FixtureCatalog()
	cat.Table("public", "users").PrimaryKey = nil
	if body := sdlType(build(cat), "type DeleteUserPayload"); strings.Contains(body, "deletedUserNodeId") {
		t.Errorf("table without a nodeId must not get deletedUserNodeId:\n%s", body)
	}
}
