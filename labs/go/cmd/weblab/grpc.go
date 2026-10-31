package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// gRPC is HTTP/2 with conventions (https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md):
//
//	request   POST /package.Service/Method, content-type: application/grpc, te: trailers,
//	          optional grpc-timeout; body = length-prefixed messages
//	message   1 byte compressed flag, 4 bytes big-endian length, the protobuf bytes
//	response  HTTP 200, content-type: application/grpc, messages, then TRAILERS
//	          grpc-status (0 = OK) and grpc-message — the RPC's real result
//
// This file implements both ends by hand for the course's echo.proto, whose
// messages are google.protobuf.StringValue: field 1, a string.

// Status codes from https://grpc.github.io/grpc/core/md_doc_statuscodes.html.
var statusNames = map[int]string{
	0: "OK", 1: "CANCELLED", 2: "UNKNOWN", 3: "INVALID_ARGUMENT", 4: "DEADLINE_EXCEEDED", 5: "NOT_FOUND",
	6: "ALREADY_EXISTS", 7: "PERMISSION_DENIED", 8: "RESOURCE_EXHAUSTED", 9: "FAILED_PRECONDITION",
	10: "ABORTED", 11: "OUT_OF_RANGE", 12: "UNIMPLEMENTED", 13: "INTERNAL", 14: "UNAVAILABLE",
	15: "DATA_LOSS", 16: "UNAUTHENTICATED",
}

// HTTPToGRPC is the gRPC spec's mapping for responses that are not gRPC at
// all — what a client reports when a proxy or load balancer answers itself.
func HTTPToGRPC(httpStatus int) int {
	switch httpStatus {
	case 400:
		return 13
	case 401:
		return 16
	case 403:
		return 7
	case 404:
		return 12
	case 429, 502, 503, 504:
		return 14
	}
	return 2
}

// EncodeString is a StringValue message: tag 0x0a (field 1, length-delimited),
// varint length, the bytes. The empty string encodes as zero bytes.
func EncodeString(s string) []byte {
	if s == "" {
		return nil
	}
	b := []byte{0x0a}
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// DecodeString reads a StringValue, skipping unknown fields.
func DecodeString(b []byte) (string, error) {
	var out string
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return "", errors.New("bad tag")
		}
		b = b[n:]
		switch tag & 7 {
		case 0:
			_, n = binary.Uvarint(b)
			if n <= 0 {
				return "", errors.New("bad varint")
			}
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return "", errors.New("bad length")
			}
			if tag>>3 == 1 {
				out = string(b[n : n+int(l)])
			}
			b = b[n+int(l):]
		default:
			return "", fmt.Errorf("unsupported wire type %d", tag&7)
		}
	}
	return out, nil
}

// Frame prefixes a message for the gRPC body.
func Frame(msg []byte) []byte {
	b := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(b[1:], uint32(len(msg)))
	return append(b, msg...)
}

// ReadMessage reads one length-prefixed message.
func ReadMessage(r io.Reader) ([]byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if h[0] != 0 {
		return nil, errors.New("compressed messages are not supported")
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > 4<<20 {
		return nil, fmt.Errorf("message of %d bytes exceeds the 4 MiB limit", n)
	}
	msg := make([]byte, n)
	_, err := io.ReadFull(r, msg)
	return msg, err
}

// ParseTimeout reads a grpc-timeout value: digits and a unit H M S m u n.
func ParseTimeout(v string) (time.Duration, error) {
	if len(v) < 2 || len(v) > 9 {
		return 0, fmt.Errorf("bad grpc-timeout %q", v)
	}
	n, err := strconv.ParseInt(v[:len(v)-1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad grpc-timeout %q", v)
	}
	unit := map[byte]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second, 'm': time.Millisecond, 'u': time.Microsecond, 'n': time.Nanosecond}[v[len(v)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("bad grpc-timeout unit in %q", v)
	}
	return time.Duration(n) * unit, nil
}

// FormatTimeout writes a duration as grpc-timeout, in milliseconds.
func FormatTimeout(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) + "m" }

// serveGRPC answers /course.Echo/{Echo,Slow,Watch} the way grpc-go does.
func serveGRPC(w http.ResponseWriter, r *http.Request, log *logger) {
	if r.ProtoMajor != 2 {
		// grpc-go's ServeHTTP answers exactly this to HTTP/1.x (older
		// releases used 400 Bad Request).
		http.Error(w, "gRPC requires HTTP/2", http.StatusHTTPVersionNotSupported)
		return
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		http.Error(w, fmt.Sprintf("invalid gRPC request content-type %q", r.Header.Get("Content-Type")), http.StatusUnsupportedMediaType)
		return
	}
	ctx := r.Context()
	if v := r.Header.Get("grpc-timeout"); v != "" {
		d, err := ParseTimeout(v)
		if err != nil {
			trailersOnly(w, 13, err.Error())
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	method := strings.TrimPrefix(r.URL.Path, "/course.Echo/")
	if method != "Echo" && method != "Slow" && method != "Watch" {
		trailersOnly(w, 12, "unknown method "+method+" for service course.Echo")
		return
	}
	raw, err := ReadMessage(r.Body)
	if err != nil {
		trailersOnly(w, 13, "reading request: "+err.Error())
		return
	}
	in, err := DecodeString(raw)
	if err != nil {
		trailersOnly(w, 13, "decoding request: "+err.Error())
		return
	}
	log.Printf("grpc %s(%q) timeout=%s", method, in, orDash(r.Header.Get("grpc-timeout")))
	if in == "" {
		trailersOnly(w, 3, "value must not be empty")
		return
	}
	w.Header().Set("Content-Type", "application/grpc")
	if method != "Slow" {
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
	}
	f, _ := w.(http.Flusher)
	finish := func(code int, msg string) {
		w.Header().Set("Grpc-Status", strconv.Itoa(code))
		if msg != "" {
			w.Header().Set("Grpc-Message", percentEncode(msg))
		}
		log.Printf("grpc %s -> %s", method, statusNames[code])
	}
	switch method {
	case "Echo":
		_, _ = w.Write(Frame(EncodeString(in)))
		finish(0, "")
	case "Slow":
		start := time.Now()
		select {
		case <-time.After(2 * time.Second):
			w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
			_, _ = w.Write(Frame(EncodeString(in)))
			finish(0, "")
		case <-ctx.Done():
			// Whichever comes first — our copy of the deadline or the client's
			// RST_STREAM when its own copy expires — the work stops.
			reason := "client cancelled (RST_STREAM)"
			if r.Context().Err() == nil {
				reason = "deadline exceeded"
			}
			log.Printf("grpc %s: work stopped after %s: %s", method, time.Since(start).Round(time.Millisecond), reason)
			if r.Context().Err() == nil {
				trailersOnly(w, 4, "deadline exceeded after "+r.Header.Get("grpc-timeout"))
			}
		}
	case "Watch":
		w.WriteHeader(http.StatusOK)
		for i := 1; i <= 5; i++ {
			if i > 1 {
				select {
				case <-time.After(300 * time.Millisecond):
				case <-ctx.Done():
					if r.Context().Err() != nil {
						log.Printf("grpc %s: client cancelled after %d messages", method, i-1)
						return
					}
					finish(4, "deadline exceeded")
					return
				}
			}
			_, _ = w.Write(Frame(EncodeString(fmt.Sprintf("%s %d", in, i))))
			if f != nil {
				f.Flush()
			}
		}
		finish(0, "")
	}
}

// trailersOnly is gRPC's error-before-any-message response: HTTP 200 with
// grpc-status in the headers and no body.
func trailersOnly(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Set("Grpc-Status", strconv.Itoa(code))
	w.Header().Set("Grpc-Message", percentEncode(msg))
	w.WriteHeader(http.StatusOK)
}

// GRPCCall is one call made by `weblab grpc`, printed frame by frame.
type GRPCCall struct {
	Target  string // host:port
	TLS     bool
	CAFile  string
	Method  string // e.g. course.Echo/Echo
	Value   string
	Timeout time.Duration
	Out     io.Writer
}

// Run makes the call and returns the gRPC status code.
func (c GRPCCall) Run(ctx context.Context) (int, error) {
	var p http.Protocols
	tr := &http.Transport{Proxy: nil, Protocols: &p}
	scheme := "http"
	if c.TLS {
		scheme = "https"
		p.SetHTTP2(true)
		cfg := &tls.Config{}
		if c.CAFile != "" {
			pemData, err := os.ReadFile(c.CAFile)
			if err != nil {
				return 2, err
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pemData)
			cfg.RootCAs = pool
		}
		tr.TLSClientConfig = cfg
	} else {
		p.SetUnencryptedHTTP2(true) // h2c with prior knowledge, as gRPC clients do
	}
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	body := Frame(EncodeString(c.Value))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scheme+"://"+c.Target+"/"+strings.TrimPrefix(c.Method, "/"), bytes.NewReader(body))
	if err != nil {
		return 2, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("Te", "trailers")
	if c.Timeout > 0 {
		req.Header.Set("Grpc-Timeout", FormatTimeout(c.Timeout))
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
		req = req.WithContext(ctx)
	}
	start := time.Now()
	fmt.Fprintf(c.Out, "> POST %s  (HTTP/2 HEADERS)\n", req.URL.Path)
	for _, k := range []string{"Content-Type", "Te", "Grpc-Timeout"} {
		if v := req.Header.Get(k); v != "" {
			fmt.Fprintf(c.Out, ">   %s: %s\n", strings.ToLower(k), v)
		}
	}
	fmt.Fprintf(c.Out, "> DATA % x  (flag 0, length %d, then the protobuf bytes)\n", body, len(body)-5)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(c.Out, "status: DEADLINE_EXCEEDED (4) — client gave up after %s and reset the stream\n", time.Since(start).Round(time.Millisecond))
			return 4, nil
		}
		fmt.Fprintf(c.Out, "status: UNAVAILABLE (14) — %v\n", err)
		return 14, nil
	}
	defer resp.Body.Close()
	fmt.Fprintf(c.Out, "< %s %d  (HEADERS)\n", resp.Proto, resp.StatusCode)
	for _, k := range []string{"Content-Type", "Grpc-Status", "Grpc-Message"} {
		if v := resp.Header.Get(k); v != "" {
			fmt.Fprintf(c.Out, "<   %s: %s\n", strings.ToLower(k), v)
		}
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/grpc") {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		code := HTTPToGRPC(resp.StatusCode)
		fmt.Fprintf(c.Out, "<   body: %q\n", strings.TrimSpace(string(b)))
		fmt.Fprintf(c.Out, "status: %s (%d) — not a gRPC response; HTTP %d maps to %s\n", statusNames[code], code, resp.StatusCode, statusNames[code])
		return code, nil
	}
	if v := resp.Header.Get("Grpc-Status"); v != "" { // trailers-only
		code, _ := strconv.Atoi(v)
		msg, _ := url.PathUnescape(resp.Header.Get("Grpc-Message"))
		fmt.Fprintf(c.Out, "status: %s (%d) %s  (trailers-only response, HTTP 200)\n", statusNames[code], code, msg)
		return code, nil
	}
	for {
		raw, err := ReadMessage(resp.Body)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintf(c.Out, "status: DEADLINE_EXCEEDED (4) — client gave up after %s and reset the stream\n", time.Since(start).Round(time.Millisecond))
				return 4, nil
			}
			break
		}
		s, _ := DecodeString(raw)
		fmt.Fprintf(c.Out, "< DATA message %q  (+%s)\n", s, time.Since(start).Round(time.Millisecond))
	}
	code, err := strconv.Atoi(resp.Trailer.Get("Grpc-Status"))
	if err != nil {
		fmt.Fprintf(c.Out, "status: UNKNOWN (2) — stream ended without grpc-status trailers\n")
		return 2, nil
	}
	msg, _ := url.PathUnescape(resp.Trailer.Get("Grpc-Message"))
	fmt.Fprintf(c.Out, "< TRAILERS grpc-status: %d%s\n", code, map[bool]string{true: ", grpc-message: " + msg, false: ""}[msg != ""])
	fmt.Fprintf(c.Out, "status: %s (%d) after %s\n", statusNames[code], code, time.Since(start).Round(time.Millisecond))
	return code, nil
}

// percentEncode is grpc-message's encoding: printable ASCII except '%' is
// sent as is, everything else as %XX.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c <= 0x7e && c != '%' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
