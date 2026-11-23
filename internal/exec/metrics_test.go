package exec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func scrapeMetrics(t *testing.T, m *metrics.Metrics) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := map[string]float64{}
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("bad sample %q", line)
		}
		out[line[:i]] = v
	}
	return out
}

func metricsExecutor(t *testing.T, opts Options) (*Executor, *metrics.Metrics) {
	t.Helper()
	m, err := metrics.New(metrics.Options{Namespace: "pdbq"})
	if err != nil {
		t.Fatal(err)
	}
	opts.Metrics, err = NewMetrics(m)
	if err != nil {
		t.Fatal(err)
	}
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	opts.Logger = slog.New(slog.DiscardHandler)
	return New(nil, built, compile.New(built).Compile, nil, opts), m
}

func TestMetricsDocumentCacheAndCost(t *testing.T) {
	e, m := metricsExecutor(t, Options{DocumentCacheSize: 10, MaxCost: 50})
	ctx := context.Background()
	q := `{userById(id: 1) {id}}`
	for range 3 {
		if _, _, err := e.prepare(ctx, Request{Query: q}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.prepare(ctx, Request{Query: `{allUsers(first: 100) {nodes {id email}}}`}); err == nil {
		t.Fatal("want a cost-limit rejection")
	}
	got := scrapeMetrics(t, m)
	for series, want := range map[string]float64{
		`pdbq_document_cache_requests_total{result="hit"}`:  2,
		`pdbq_document_cache_requests_total{result="miss"}`: 2,
		`pdbq_cost_limit_rejections_total`:                  1,
	} {
		if got[series] != want {
			t.Errorf("%s = %v, want %v", series, got[series], want)
		}
	}
}

func TestMetricsCompileAndFieldErrors(t *testing.T) {
	e, m := metricsExecutor(t, Options{})
	ctx := context.Background()
	_, op, err := e.prepare(ctx, Request{Query: `{userById(id: 1) {id}}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.compileField(ctx, op, collectRootFields(op.Definition.SelectionSet, nil, nil)[0]); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		&pgconn.PgError{Code: "23505"},
		&pgconn.PgError{Code: "42501"},
		gqlerror.Errorf("no row matched"),
		errors.New("boom"),
	} {
		if ge := e.gqlError(err, nil); ge == nil {
			t.Fatalf("gqlError(%v) = nil", err)
		}
	}
	got := scrapeMetrics(t, m)
	for series, want := range map[string]float64{
		`pdbq_compile_duration_seconds_count`:              1,
		`pdbq_field_errors_total{class="conflict"}`:        1,
		`pdbq_field_errors_total{class="permission"}`:      1,
		`pdbq_field_errors_total{class="user_input"}`:      1,
		`pdbq_field_errors_total{class="internal"}`:        1,
		`pdbq_document_cache_requests_total{result="hit"}`: 0,
	} {
		if got[series] != want {
			t.Errorf("%s = %v, want %v", series, got[series], want)
		}
	}
}

func TestErrorClass(t *testing.T) {
	for err, want := range map[error]string{
		&compile.UserError{}:                        "user_input",
		fmt.Errorf("x: %w", gqlerror.Errorf("bad")): "user_input",
		&pgconn.PgError{Code: "22P02"}:              "user_input",
		&pgconn.PgError{Code: "40001"}:              "conflict",
		context.DeadlineExceeded:                    "timeout",
		errors.New("boom"):                          "internal",
	} {
		if got := ErrorClass(err); got != want {
			t.Errorf("ErrorClass(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestNilMetricsRecordNothing(t *testing.T) {
	var m *Metrics
	if m.observer() != nil {
		t.Fatal("nil Metrics must give a nil observer")
	}
	m.documentLookup(true)
	m.costRejected()
	m.fieldError(errors.New("x"))
	if got, err := NewMetrics(nil); got != nil || err != nil {
		t.Fatalf("NewMetrics(nil) = %v, %v", got, err)
	}
}

func TestBatchTxReportsRetryAttempts(t *testing.T) {
	e, m := metricsExecutor(t, Options{TxRetries: 2, TxPerRequest: true})
	calls := 0
	e.queryTx = func(ctx context.Context, _ pgexec.ConnAcquirer, opts pgexec.TxOptions, _ pgexec.Identity, _ ...pgexec.Stmt) error {
		calls++
		var err error
		if calls < 3 {
			err = &pgconn.PgError{Code: "40001"}
		}
		opts.Observer.ObserveQuery(ctx, pgexec.QueryEvent{Op: pgexec.OpQueryTx, Err: err})
		return err
	}
	res := e.Execute(context.Background(), Request{Query: `{userById(id: 1) {id}}`})
	if calls != 3 {
		t.Fatalf("queryTx calls = %d, want 3 (errors %v)", calls, res.Errors)
	}
	got := scrapeMetrics(t, m)
	for series, want := range map[string]float64{
		`pdbq_db_duration_seconds_count{op="query_tx"}`:           3,
		`pdbq_db_retries_total{op="query_tx"}`:                    2,
		`pdbq_db_errors_total{op="query_tx",sqlstate_class="40"}`: 2,
	} {
		if got[series] != want {
			t.Errorf("%s = %v, want %v", series, got[series], want)
		}
	}
}
