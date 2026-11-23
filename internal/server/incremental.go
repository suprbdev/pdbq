package server

import (
	"context"
	"mime"
	"net/http"
	"strconv"
	"strings"

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
// server.request_timeout bounds the whole response.
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
	rc := http.NewResponseController(w)
	started := false
	err := ex.ExecuteIncrementalRequest(ctx, req, format, func(body []byte, incremental bool) error {
		if !incremental {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write(append(body, '\n'))
			return err
		}
		if !started {
			started = true
			ct := `multipart/mixed; boundary="` + multipartBoundary + `"; incrementalSpec=v0.2`
			if format == exec.IncrementalDefer20220824 {
				ct = `multipart/mixed; boundary="` + multipartBoundary + `"; deferSpec=20220824`
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("\r\n--" + multipartBoundary)); err != nil {
				return err
			}
		}
		part := make([]byte, 0, len(body)+96)
		part = append(part, "\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: "...)
		part = strconv.AppendInt(part, int64(len(body)), 10)
		part = append(part, "\r\n\r\n"...)
		part = append(part, body...)
		part = append(part, "\r\n--"+multipartBoundary...)
		if _, err := w.Write(part); err != nil {
			return err
		}
		return rc.Flush()
	})
	if err != nil && ctx.Err() == nil {
		s.log.Debug("incremental response ended early", "err", err)
	}
	if started {
		if _, err := w.Write([]byte("--\r\n")); err == nil {
			_ = rc.Flush()
		}
	}
}
