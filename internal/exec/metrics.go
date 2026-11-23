package exec

import (
	"context"
	"errors"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
)

// Metrics are the executor's Prometheus instruments. A nil *Metrics, and
// any nil field, records nothing.
type Metrics struct {
	// Core records compile time and, through its DB observer, database
	// operations.
	Core *metrics.Metrics
	// DocumentCache counts document cache lookups by result (hit, miss).
	DocumentCache *metrics.Counter
	// CostRejections counts operations rejected by limits.max_cost.
	CostRejections *metrics.Counter
	// FieldErrors counts errors raised while executing root fields, by
	// class (see ErrorClass).
	FieldErrors *metrics.Counter
}

// NewMetrics registers pdbq's product counters on m (named with m's
// namespace, pdbq by default): document_cache_requests_total{result},
// cost_limit_rejections_total and field_errors_total{class}. A nil m gives
// a nil *Metrics.
func NewMetrics(m *metrics.Metrics) (*Metrics, error) {
	if m == nil {
		return nil, nil
	}
	docs, err := m.NewCounter("document_cache_requests_total", "Document cache lookups by result (hit, miss).", "result")
	if err != nil {
		return nil, err
	}
	docs.Init("hit")
	docs.Init("miss")
	cost, err := m.NewCounter("cost_limit_rejections_total", "Operations rejected for exceeding limits.max_cost.")
	if err != nil {
		return nil, err
	}
	fieldErrs, err := m.NewCounter("field_errors_total", "Errors raised while executing root fields, by class (user_input, permission, conflict, resource, timeout, internal).", "class")
	if err != nil {
		return nil, err
	}
	return &Metrics{Core: m, DocumentCache: docs, CostRejections: cost, FieldErrors: fieldErrs}, nil
}

func (m *Metrics) observer() pgexec.Observer {
	if m == nil {
		return nil
	}
	return m.Core.DBObserver()
}

func (m *Metrics) documentLookup(hit bool) {
	if m == nil {
		return
	}
	if hit {
		m.DocumentCache.Inc("hit")
	} else {
		m.DocumentCache.Inc("miss")
	}
}

func (m *Metrics) costRejected() {
	if m == nil {
		return
	}
	m.CostRejections.Inc()
}

func (m *Metrics) fieldError(err error) {
	if m == nil {
		return
	}
	m.FieldErrors.Inc(ErrorClass(err))
}

func (m *Metrics) compiled(start time.Time) {
	if m == nil {
		return
	}
	m.Core.ObserveCompile(time.Since(start))
}

// ErrorClass is the class label of pdbq_field_errors_total: user_input for
// GraphQL and client-safe compile errors, otherwise the pgexec.Classify
// class name (user_input, permission, conflict, resource, timeout,
// internal).
func ErrorClass(err error) string {
	var gerr *gqlerror.Error
	var uerr *compile.UserError
	if errors.As(err, &gerr) || errors.As(err, &uerr) {
		return pgexec.UserInput.String()
	}
	return pgexec.Classify(err, pgexec.Prod).Class.String()
}

// attemptObserver reports a QueryTx call that batchTx itself retries as
// the given attempt.
type attemptObserver struct {
	next    pgexec.Observer
	attempt int
}

func (o attemptObserver) ObserveQuery(ctx context.Context, ev pgexec.QueryEvent) {
	ev.Attempt = o.attempt
	o.next.ObserveQuery(ctx, ev)
}
