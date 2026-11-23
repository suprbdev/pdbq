package server

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/suprbdev/pdbq/internal/exec"
)

// multipartBoundary is the boundary of incremental responses, the one
// GraphQL Yoga and Apollo Router use.
const multipartBoundary = "-"

// incrementalAccept negotiates incremental delivery from the Accept header
// of r. ok is true when it lists multipart/mixed: the format is the
// deferSpec=20220824 shape when that entry asks for it and the
// incrementalSpec=v0.2 shape otherwise. unsupported is true when every
// multipart/mixed entry names a deferSpec or incrementalSpec pdbq does not
// serve.
func incrementalAccept(r *http.Request) (format exec.IncrementalFormat, ok, unsupported bool) {
	for _, v := range r.Header.Values("Accept") {
		for part := range strings.SplitSeq(v, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || mt != "multipart/mixed" {
				continue
			}
			f, known := exec.IncrementalV02, true
			if spec, has := params["deferspec"]; has {
				f, known = exec.IncrementalDefer20220824, spec == "20220824"
			}
			if spec, has := params["incrementalspec"]; has && known {
				f, known = exec.IncrementalV02, strings.EqualFold(spec, "v0.2")
			}
			if known {
				return f, true, false
			}
			unsupported = true
		}
	}
	return 0, false, unsupported
}

// streamsIncremental reports whether r is a GraphQL request answered
// through serveIncremental, outside the request timeout handler and gzip,
// which would buffer its payloads.
func (s *Server) streamsIncremental(r *http.Request) bool {
	if !s.cfg.IncrementalDelivery || r.URL.Path != "/graphql" {
		return false
	}
	_, ok, unsupported := incrementalAccept(r)
	return ok || unsupported
}

// serveIncremental answers a GraphQL request whose Accept header lists
// multipart/mixed. A query delivered incrementally is a multipart/mixed
// response with one application/json part per payload, flushed as it is
// produced; any other result is a plain application/json response.
// server.request_timeout bounds the whole response, and
// server.incremental_write_timeout each payload: a client that stops
// reading gets its response aborted and its transaction rolled back. Beyond
// server.incremental_max_responses responses in flight, the request is
// answered by one result.
func (s *Server) serveIncremental(w http.ResponseWriter, r *http.Request, ex *exec.Executor, req exec.Request) {
	format, _, unsupported := incrementalAccept(r)
	if unsupported {
		writeError(w, http.StatusNotAcceptable, "unsupported incremental delivery format: accept multipart/mixed;incrementalSpec=v0.2 or multipart/mixed;deferSpec=20220824")
		return
	}
	ctx := r.Context()
	if t := s.cfg.RequestTimeout; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	out := &boundedWriter{w: w, rc: http.NewResponseController(w), ctx: ctx, timeout: s.cfg.IncrementalWriteTimeout}
	defer func() { _ = out.wait() }()
	if !s.incremental.acquire(s.cfg.IncrementalMaxResponses) {
		body, err := ex.Execute(ctx, req).AppendJSON(nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = out.write(append(body, '\n'))
		return
	}
	defer s.incremental.release()
	started := false
	err := ex.ExecuteIncrementalRequest(ctx, req, format, func(body []byte, incremental bool) error {
		if out.err != nil {
			return out.err
		}
		if !incremental {
			w.Header().Set("Content-Type", "application/json")
			return out.write(append(body, '\n'))
		}
		part := make([]byte, 0, len(body)+96)
		if !started {
			started = true
			ct := `multipart/mixed; boundary="` + multipartBoundary + `"; incrementalSpec=v0.2`
			if format == exec.IncrementalDefer20220824 {
				ct = `multipart/mixed; boundary="` + multipartBoundary + `"; deferSpec=20220824`
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			part = append(part, "\r\n--"+multipartBoundary...)
		}
		part = append(part, "\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: "...)
		part = strconv.AppendInt(part, int64(len(body)), 10)
		part = append(part, "\r\n\r\n"...)
		part = append(part, body...)
		part = append(part, "\r\n--"+multipartBoundary...)
		return out.write(part)
	})
	if err != nil && ctx.Err() == nil {
		s.log.Debug("incremental response ended early", "err", err)
	}
	if started {
		_ = out.write([]byte("--\r\n"))
	}
}

// errWriteStalled fails a payload the client did not take within
// server.incremental_write_timeout.
var errWriteStalled = errors.New("incremental response: client stopped reading")

// boundedWriter writes and flushes the payloads of one incremental
// response, none taking longer than timeout (or, while ctx is live, than
// ctx's deadline); a timeout <= 0 leaves only ctx's deadline. It sets that
// deadline on the connection, and also returns errWriteStalled when the
// deadline passes with the write still blocked (a ResponseWriter that does
// not support deadlines), so the caller can roll its transaction back. A
// failed write fails every later one.
type boundedWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	ctx     context.Context
	timeout time.Duration
	err     error
	// stalled receives the outcome of a write still blocked when it timed
	// out; the handler must not return before it.
	stalled chan error
}

func (b *boundedWriter) write(p []byte) error {
	if b.err != nil {
		return b.err
	}
	var deadline time.Time
	if b.timeout > 0 {
		deadline = time.Now().Add(b.timeout)
	}
	if d, ok := b.ctx.Deadline(); ok && b.ctx.Err() == nil && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	_ = b.rc.SetWriteDeadline(deadline)
	done := make(chan error, 1)
	go func() {
		_, err := b.w.Write(p)
		if err == nil {
			err = b.rc.Flush()
		}
		done <- err
	}()
	var expired <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case b.err = <-done:
	case <-expired:
		b.stalled, b.err = done, errWriteStalled
	}
	return b.err
}

// wait blocks until a write that timed out has returned, and returns its
// error.
func (b *boundedWriter) wait() error {
	if b.stalled == nil {
		return nil
	}
	return <-b.stalled
}

// slots counts holders against a limit.
type slots struct{ n atomic.Int64 }

// acquire takes a slot unless limit (> 0) are taken; limit <= 0 is no
// limit.
func (l *slots) acquire(limit int) bool {
	if l.n.Add(1) > int64(limit) && limit > 0 {
		l.n.Add(-1)
		return false
	}
	return true
}

func (l *slots) release() { l.n.Add(-1) }
