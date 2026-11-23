package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

const contractsSchema = "contracts_e2e"

// setupContracts creates the contracts_e2e schema: customers (email hidden
// from public), invoices (internal and partner only), audits (internal
// only) and two functions, one tagged into the default-exclude partner
// contract.
func setupContracts(t *testing.T) {
	t.Helper()
	conn := testConn(t)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DROP SCHEMA IF EXISTS `+contractsSchema+` CASCADE`)
		_, _ = conn.Exec(bg, `DROP EVENT TRIGGER IF EXISTS pdbq_watch; DROP EVENT TRIGGER IF EXISTS pdbq_watch_drop; DROP FUNCTION IF EXISTS pdbq_watch_notify()`)
	})
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+contractsSchema+` CASCADE`,
		`CREATE SCHEMA `+contractsSchema,
		`SET search_path TO `+contractsSchema,
		`CREATE TABLE customers (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL,
			email text UNIQUE, tier text)`,
		`COMMENT ON TABLE customers IS '@behavior +contract:partner'`,
		`COMMENT ON COLUMN customers.email IS E'@behavior -contract:public\nContact address.'`,
		`CREATE TABLE invoices (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			customer_id int NOT NULL REFERENCES customers (id), amount int NOT NULL)`,
		`COMMENT ON TABLE invoices IS '@contract internal, partner'`,
		`CREATE TABLE audits (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, note text)`,
		`COMMENT ON TABLE audits IS '@contract internal'`,
		`INSERT INTO customers (name, email, tier) VALUES ('acme', 'ops@acme.test', 'gold'), ('globex', 'it@globex.test', 'silver')`,
		`INSERT INTO invoices (customer_id, amount) VALUES (1, 100), (1, 250), (2, 75)`,
		`INSERT INTO audits (note) VALUES ('created')`,
		`CREATE FUNCTION gold_customers() RETURNS SETOF customers LANGUAGE sql STABLE AS $$ SELECT * FROM `+contractsSchema+`.customers WHERE tier = 'gold' $$`,
		`COMMENT ON FUNCTION gold_customers() IS '@behavior +contract:partner'`,
		`CREATE FUNCTION audit_count() RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT count(*) FROM `+contractsSchema+`.audits $$`,
		`RESET search_path`,
	)
}

func contractsServer(t *testing.T, watch bool) (*pdbq.App, string) {
	t.Helper()
	return contractsServerWith(t, watch, nil)
}

// contractsServerWith is contractsServer running ddl on the contracts_e2e
// schema after its setup and applying configure to the config.
func contractsServerWith(t *testing.T, watch bool, configure func(*config.Config), ddl ...string) (*pdbq.App, string) {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	setupContracts(t)
	if len(ddl) > 0 {
		execAll(t, testConn(t), ddl...)
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	noIdentity(&cfg)
	cfg.Errors.Detail = "dev"
	cfg.Plugins.Disabled = []string{"simple_names"}
	cfg.Schema.Schemas = []string{contractsSchema}
	cfg.Filters.IndexedOnly = false
	cfg.Server.ExposeSchema = true
	cfg.Metrics.Enabled = true
	cfg.Contracts = []config.Contract{
		{Name: "internal", Path: "/"},
		{Name: "public"},
		{Name: "partner", Default: "exclude", Path: "/partners"},
	}
	if watch {
		cfg.Watch.Enabled = true
		cfg.Watch.PollInterval = 200 * time.Millisecond
	}
	if configure != nil {
		configure(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return app, hs.URL
}

func getSDL(t *testing.T, url string) string {
	t.Helper()
	body, err := httpGet(url + "/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func sdlType(sdl, name string) bool {
	return regexp.MustCompile(`(?m)^type ` + name + `\b`).MatchString(sdl)
}

func TestContracts(t *testing.T) {
	_, base := contractsServer(t, false)
	public, partner := base+"/public", base+"/partners"

	t.Run("schemas", func(t *testing.T) {
		internal := getSDL(t, base)
		for _, typ := range []string{"Customer", "Invoice", "Audit"} {
			if !sdlType(internal, typ) {
				t.Errorf("internal schema lacks type %s", typ)
			}
		}
		if !strings.Contains(internal, "email: String") || !strings.Contains(internal, "auditCount") {
			t.Error("internal schema lacks email or auditCount")
		}

		pub := getSDL(t, public)
		if strings.Contains(strings.ToLower(pub), "email") {
			t.Error("public schema mentions the hidden email column")
		}
		if !sdlType(pub, "Customer") || sdlType(pub, "Invoice") || sdlType(pub, "Audit") {
			t.Error("public schema: want Customer only")
		}
		if strings.Contains(pub, "invoicesByCustomerId") {
			t.Error("public schema keeps the relation to the hidden invoices table")
		}

		part := getSDL(t, partner)
		if !sdlType(part, "Customer") || !sdlType(part, "Invoice") || sdlType(part, "Audit") {
			t.Error("partner schema: want Customer and Invoice only")
		}
		if !strings.Contains(part, "goldCustomers") || strings.Contains(part, "auditCount") {
			t.Error("partner schema: want goldCustomers and no auditCount")
		}
	})

	t.Run("hidden column absent from every surface", func(t *testing.T) {
		for _, q := range []string{
			`{ allCustomers { nodes { email } } }`,
			`{ allCustomers(filter: {email: {equalTo: "ops@acme.test"}}) { totalCount } }`,
			`{ allCustomers(condition: {email: "ops@acme.test"}) { totalCount } }`,
			`{ allCustomers(orderBy: EMAIL_ASC) { totalCount } }`,
			`{ customerByEmail(email: "ops@acme.test") { id } }`,
			`mutation { createCustomer(input: {customer: {name: "x", email: "x@x.test"}}) { customer { id } } }`,
			`mutation { updateCustomerById(input: {id: 1, customerPatch: {email: "x@x.test"}}) { customer { id } } }`,
		} {
			if res := post(t, public, q, nil); len(res.Errors) == 0 {
				t.Errorf("public accepted %s", q)
			}
		}
		res := post(t, public, `{ allCustomers(orderBy: NAME_ASC) { nodes { name tier } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["allCustomers"]); got != `{"nodes":[{"name":"acme","tier":"gold"},{"name":"globex","tier":"silver"}]}` {
			t.Errorf("public customers = %s", got)
		}
	})

	t.Run("the same column served elsewhere", func(t *testing.T) {
		res := post(t, base, `{ customerByEmail(email: "ops@acme.test") { name } allAudits { totalCount } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["customerByEmail"]); got != `{"name":"acme"}` {
			t.Errorf("internal customerByEmail = %s", got)
		}
		res = post(t, partner, `{ allInvoices(orderBy: ID_ASC) { nodes { amount customer { email } } } goldCustomers { name } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["goldCustomers"]); got != `[{"name":"acme"}]` {
			t.Errorf("partner goldCustomers = %s", got)
		}
		if res := post(t, partner, `{ allAudits { totalCount } }`, nil); len(res.Errors) == 0 {
			t.Error("partner reached the internal audits table")
		}
		if res := post(t, partner, `{ auditCount }`, nil); len(res.Errors) == 0 {
			t.Error("partner reached the untagged audit_count function")
		}
	})

	t.Run("routes", func(t *testing.T) {
		for path, want := range map[string]int{"/nope/graphql": http.StatusNotFound, "/healthz": http.StatusOK, "/readyz": http.StatusOK} {
			resp, err := http.Get(base + path)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != want {
				t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
			}
		}
	})

	t.Run("metrics labelled by contract", func(t *testing.T) {
		body, err := httpGet(base + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		got := samples(t, string(body))
		for _, series := range []string{
			`pdbq_http_requests_total{contract="public",method="POST",route="graphql",status_class="2xx"}`,
			`pdbq_http_requests_total{contract="partner",method="POST",route="graphql",status_class="2xx"}`,
			`pdbq_http_requests_total{contract="internal",method="POST",route="graphql",status_class="2xx"}`,
		} {
			if got[series] < 1 {
				t.Errorf("%s = %v, want >= 1", series, got[series])
			}
		}
		if _, ok := got["pdbq_db_pool_max_conns"]; !ok {
			t.Error("pool metrics missing")
		}
	})
}

func TestContractsWatchRebuildsEveryContract(t *testing.T) {
	app, base := contractsServer(t, true)
	select {
	case <-app.WatchReady():
	case <-time.After(watchDeadline):
		t.Fatal("watcher never became ready")
	}
	conn := testConn(t)
	execAll(t, conn,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.tier IS '@behavior -contract:partner'`,
		`COMMENT ON TABLE `+contractsSchema+`.audits IS '@contract internal, public'`,
	)
	deadline := time.Now().Add(watchDeadline)
	for {
		part, pub := getSDL(t, base+"/partners"), getSDL(t, base+"/public")
		if !strings.Contains(part, "tier") && sdlType(pub, "Audit") {
			if !strings.Contains(getSDL(t, base), "tier") {
				t.Fatal("the internal contract lost the tier column")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("contracts not rebuilt: partner has tier %v, public has Audit %v", strings.Contains(part, "tier"), sdlType(pub, "Audit"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	body, err := httpGet(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	got := samples(t, string(body))
	for _, name := range []string{"internal", "public", "partner"} {
		if n := got[`pdbq_schema_rebuilds_total{contract="`+name+`",result="success"}`]; n < 1 {
			t.Errorf("%s rebuilds = %v, want >= 1", name, n)
		}
	}
}

// TestContractsKeepRLS serves the fixture through a contract with RLS on:
// the contract narrows the schema, the policies still decide the rows.
func TestContractsKeepRLS(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.Plugins.Disabled = []string{"simple_names"}
	headersIdentity(&cfg)
	cfg.Contracts = []config.Contract{{Name: "public"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	countPosts := func(headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest("POST", hs.URL+"/public/graphql", strings.NewReader(`{"query":"{allPosts(first: 100) { totalCount }}"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Data struct {
				AllPosts struct {
					TotalCount int `json:"totalCount"`
				} `json:"allPosts"`
			} `json:"data"`
			Errors []any `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Errors) > 0 {
			t.Fatalf("decode: %v, errors %v", err, out.Errors)
		}
		return out.Data.AllPosts.TotalCount
	}
	if n := countPosts(nil); n != 3 {
		t.Errorf("anonymous sees %d posts through the contract, want the 3 published", n)
	}
	if n := countPosts(map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": "1"}); n != 4 {
		t.Errorf("author 1 sees %d posts through the contract, want 4", n)
	}
}

// TestContractsIntegrityErrorsHideNames pins that an integrity violation
// whose message names a table, constraint or column a contract hides
// reaches that contract's clients as a generic message with the SQLSTATE,
// while a contract that shows them keeps PostgreSQL's message.
func TestContractsIntegrityErrorsHideNames(t *testing.T) {
	_, base := contractsServerWith(t, false, func(c *config.Config) { c.Errors.Detail = "prod" },
		`ALTER TABLE `+contractsSchema+`.customers ADD COLUMN pin text NOT NULL DEFAULT '0000'`,
		`ALTER TABLE `+contractsSchema+`.customers ALTER COLUMN pin DROP DEFAULT`,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.pin IS '@behavior -contract:public'`,
	)
	const (
		del    = `mutation { deleteCustomerById(id: 1) { clientMutationId } }`
		create = `mutation { createCustomer(input: {name: "x"}) { clientMutationId } }`
	)
	for _, tc := range []struct {
		name, url, query, want string
	}{
		{"hidden table in a foreign key error", base + "/public", del,
			`{"data":{"deleteCustomerById":null},"errors":[{"message":"foreign key violation","path":["deleteCustomerById"],"extensions":{"code":"23503"}}]}`},
		{"hidden column in a not-null error", base + "/public", create,
			`{"data":{"createCustomer":null},"errors":[{"message":"not-null violation","path":["createCustomer"],"extensions":{"code":"23502"}}]}`},
		{"visible foreign key error", base, del,
			`{"data":{"deleteCustomerById":null},"errors":[{"message":"update or delete on table \"customers\" violates foreign key constraint \"invoices_customer_id_fkey\" on table \"invoices\"","path":["deleteCustomerById"],"extensions":{"code":"23503"}}]}`},
		{"visible not-null error", base, `mutation { updateCustomerById(id: 1, patch: {pin: null}) { clientMutationId } }`,
			`{"data":{"updateCustomerById":null},"errors":[{"message":"null value in column \"pin\" of relation \"customers\" violates not-null constraint","path":["updateCustomerById"],"extensions":{"code":"23502"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := postRaw(t, tc.url, tc.query, nil); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
