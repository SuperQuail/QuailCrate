package pluginio

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
)

const mediaRangeSize int64 = 1 << 20
const maxMediaRanges = 1024

var errMediaRange = errors.New("invalid audio byte range")

type span struct{ start, end, total int64 }

func decimal(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func contentRange(h http.Header) (span, error) {
	if len(h.Values("Content-Range")) != 1 {
		return span{}, errMediaRange
	}
	unit, fields, ok := strings.Cut(h.Get("Content-Range"), " ")
	bytes, total, slash := strings.Cut(fields, "/")
	from, to, dash := strings.Cut(bytes, "-")
	start, a := decimal(from)
	end, b := decimal(to)
	size, c := decimal(total)
	if !ok || !strings.EqualFold(unit, "bytes") || !slash || !dash || !a || !b || !c || end < start || end >= size {
		return span{}, errMediaRange
	}
	return span{start, end, size}, nil
}

// A caller can request one HTTP byte range, including an open or suffix range.
// -1 marks the omitted endpoint.
func requestedRange(value string) (int64, int64, error) {
	unit, v, ok := strings.Cut(value, "=")
	l, r, dash := strings.Cut(v, "-")
	if !ok || !strings.EqualFold(unit, "bytes") || !dash || (l == "" && r == "") {
		return 0, 0, errMediaRange
	}
	start, end := int64(-1), int64(-1)
	if l != "" {
		start, ok = decimal(l)
		if !ok {
			return 0, 0, errMediaRange
		}
	}
	if r != "" {
		end, ok = decimal(r)
		if !ok || end < start || (start < 0 && end == 0) {
			return 0, 0, errMediaRange
		}
	}
	return start, end, nil
}

func matchesRequest(s span, start, end int64, explicit bool) bool {
	if start < 0 {
		start = max(int64(0), s.total-end)
		end = s.total - 1
	}
	if end < 0 {
		end = s.total - 1
	}
	end = min(end, s.total-1)
	if explicit {
		return s.start == start && s.end == end
	}
	return s.start == start && s.end <= end
}

func mediaValidator(h http.Header) (name, value string) {
	if etag := h.Get("ETag"); len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		return "ETag", etag
	}
	v := h.Get("Last-Modified")
	if _, err := http.ParseTime(v); err == nil {
		return "Last-Modified", v
	}
	return "", ""
}

type mediaPipe struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (p mediaPipe) Close() error { p.cancel(); return p.PipeReader.Close() }

type sizedMedia struct {
	io.ReadCloser
	length int64
	resume func(context.Context, int64) (io.ReadCloser, error)
}

func (s sizedMedia) Size() int64 { return s.length }

// Resume reopens a full-source response at a verified byte offset. Without a
// validator the caller must reopen from zero and compare the retained prefix.
func (s sizedMedia) Resume(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if s.resume == nil {
		return nil, errors.ErrUnsupported
	}
	return s.resume(ctx, offset)
}

// OpenMedia returns the body of a complete source by default. Each additional
// HTTP range is fetched through Stream after the preceding response is closed.
// An explicit single Range preserves HTTP semantics: a 206 returns that range
// only, while a server ignoring Range returns the full 200 response.
func (i *Instance) OpenMedia(ctx context.Context, r Request) (io.ReadCloser, error) {
	if (r.Method != "" && r.Method != http.MethodGet) || r.Body != "" {
		return nil, ErrInvalid
	}
	headers := make(map[string]string, len(r.Headers)+2)
	for k, v := range r.Headers {
		key := http.CanonicalHeaderKey(k)
		if _, duplicate := headers[key]; duplicate {
			return nil, ErrInvalid
		}
		headers[key] = v
	}
	r.Headers = headers
	value, explicit := headers["Range"]
	start, end := int64(0), mediaRangeSize-1
	if explicit {
		var err error
		start, end, err = requestedRange(value)
		if err != nil {
			return nil, err
		}
	} else {
		headers["Range"] = "bytes=0-" + strconv.FormatInt(end, 10)
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := i.Stream(ctx, r)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode == http.StatusOK && identityEncoding(resp.Header) {
		body := &streamBody{ReadCloser: resp.Body, finish: cancel}
		return i.sizedResponse(body, r, resp.Header, resp.ContentLength, !explicit), nil
	}
	if !explicit && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && resp.Header.Get("Content-Range") == "bytes */0" {
		resp.Body.Close()
		cancel()
		return io.NopCloser(strings.NewReader("")), nil
	}
	first, err := validateMediaPart(resp, start, end, explicit)
	if err != nil {
		resp.Body.Close()
		cancel()
		return nil, err
	}

	return i.rangedBody(ctx, cancel, r, resp, first, explicit), nil
}

func (i *Instance) rangedBody(ctx context.Context, cancel context.CancelFunc, r Request, resp *http.Response, first span, explicit bool) io.ReadCloser {
	reader, writer := io.Pipe()
	// A caller closing the reader or an instance shutting down must also wake a
	// writer blocked on a consumer that stopped reading.
	stopInstance := context.AfterFunc(i.ctx, cancel)
	stopWriter := context.AfterFunc(ctx, func() { writer.CloseWithError(ctx.Err()) })
	go func() {
		err := i.copyMedia(ctx, writer, r, resp, first, explicit)
		writer.CloseWithError(err)
		stopWriter()
		stopInstance()
		cancel()
	}()
	length := first.total
	if explicit {
		length = first.end - first.start + 1
	}
	return i.sizedResponse(mediaPipe{reader, cancel}, r, resp.Header, length, !explicit)
}

func (i *Instance) sizedResponse(body io.ReadCloser, r Request, h http.Header, total int64, full bool) sizedMedia {
	out := sizedMedia{ReadCloser: body, length: total}
	key, validator := mediaValidator(h)
	if !full || key == "" || total <= 0 {
		return out
	}
	out.resume = func(ctx context.Context, offset int64) (io.ReadCloser, error) {
		if offset < 0 || offset >= total {
			return nil, errMediaRange
		}
		req := r
		req.Headers = make(map[string]string, len(r.Headers)+2)
		for k, v := range r.Headers {
			req.Headers[k] = v
		}
		end := offset + min(mediaRangeSize, total-offset) - 1
		req.Headers["Range"] = "bytes=" + strconv.FormatInt(offset, 10) + "-" + strconv.FormatInt(end, 10)
		req.Headers["If-Range"] = validator
		ctx, cancel := context.WithCancel(ctx)
		resp, err := i.Stream(ctx, req)
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode == http.StatusOK && identityEncoding(resp.Header) && resp.Header.Get(key) == validator {
			resp.Body.Close()
			cancel()
			return nil, errors.ErrUnsupported
		}
		part, err := validateMediaPart(resp, offset, end, false)
		if err != nil || part.total != total || resp.Header.Get(key) != validator {
			resp.Body.Close()
			cancel()
			return nil, errMediaRange
		}
		return i.rangedBody(ctx, cancel, req, resp, part, false), nil
	}
	return out
}

func identityEncoding(header http.Header) bool {
	enc := header.Get("Content-Encoding")
	return enc == "" || strings.EqualFold(enc, "identity")
}

func validateMediaPart(resp *http.Response, start, end int64, explicit bool) (span, error) {
	if resp.StatusCode != http.StatusPartialContent || resp.Uncompressed || !identityEncoding(resp.Header) {
		return span{}, errMediaRange
	}
	part, err := contentRange(resp.Header)
	if err != nil || !matchesRequest(part, start, end, explicit) || (resp.ContentLength >= 0 && resp.ContentLength != part.end-part.start+1) {
		return span{}, errMediaRange
	}
	return part, nil
}

func (i *Instance) copyMedia(ctx context.Context, dst io.Writer, r Request, resp *http.Response, part span, explicit bool) error {
	r.Headers = maps.Clone(r.Headers)
	size := part.total
	key, validator := mediaValidator(resp.Header)
	for count := 1; ; count++ {
		n := part.end - part.start + 1
		_, err := io.CopyN(dst, resp.Body, n)
		if err == nil {
			_, tailErr := io.CopyN(io.Discard, resp.Body, 1)
			if tailErr != io.EOF {
				err = errMediaRange
			}
		}
		resp.Body.Close()
		if err != nil {
			if cause := ctx.Err(); cause != nil {
				return cause
			}
			return errMediaRange
		}
		if explicit || part.end == size-1 {
			return nil
		}
		if count >= maxMediaRanges {
			return errors.New("audio range count limit exceeded")
		}
		start := part.end + 1
		end := start + min(mediaRangeSize, size-start) - 1
		r.Headers["Range"] = "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10)
		if validator != "" {
			r.Headers["If-Range"] = validator
		}
		resp, err = i.Stream(ctx, r)
		if err != nil {
			return err
		}
		part, err = validateMediaPart(resp, start, end, false)
		if err != nil || part.total != size || (validator != "" && resp.Header.Get(key) != validator) {
			resp.Body.Close()
			return errMediaRange
		}
	}
}
