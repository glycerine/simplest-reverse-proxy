package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"log"
	"net"
	"sync"
	"time"
)

func main() {

	// set flags
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	listen := flag.Int("listen", 0, "port to listen on")
	downstream := flag.Int("downstream", 0, "downstream port to send traffic to")
	outfile := flag.String("o", "", "outfile to write logs to")
	flag.Parse()

	// validate flags
	if outfile == nil || *outfile == "" {
		panic("provide -o outfile to write to (or - for stdout)")
	}
	var of io.WriteCloser
	var err error
	if *outfile == "-" {
		of = os.Stdout
	} else {
		of, err = os.Open(*outfile)
		panicOn(err)
		defer of.Close()
	}
	if listen == nil || *listen == 0 {
		panic("provide -listen port")
	}
	if downstream == nil || *downstream == 0 {
		panic("provide -downstream port")
	}

	u := fmt.Sprintf("http://127.0.0.1:%v", *downstream)
	rpURL, err := url.Parse(u)
	panicOn(err)

	rev := makeRevProxy(rpURL, of)

	listenAddr := fmt.Sprintf("127.0.0.1:%v", *listen)
	fmt.Printf("\n revproxy listening on %s\n", listenAddr)
	err = http.ListenAndServe(listenAddr, rev)
	panicOn(err)
}

var reqNum int

func makeRevProxy(target *url.URL, of io.WriteCloser) *ReverseProxy {
	targetQuery := target.RawQuery
	p := &ReverseProxy{of: of}
	director := func(req *http.Request) {
		dump, err := httputil.DumpRequest(req, true)
		if err != nil {
			fmt.Println(err)
		} else {
			// ignore keepalives
			if !bytes.HasSuffix(dump, []byte(`{"size":1000,"sort":[{"buildNum":{"order":"desc","ignore_unmapped":true}}]}`)) && bytes.Contains(dump, []byte("Content-Length: ")) {

				p.interesting = true
				fmt.Printf("\n>>>>>>>> director function called. copying request %v\n",
					reqNum)

				two := bytes.SplitN(dump, []byte("\r\n\r\n"), 2)
				prettyDone := false
				//fmt.Printf("len two = %v\n", len(two))
				if len(two) > 0 {
					//fmt.Printf("two[0]: %#v\n", string(two[0]))
					//fmt.Printf("two[1]: %#v\n", two[1])
				}
				if len(two) == 2 {
					of.Write(two[0])
					of.Write([]byte("\n\n"))

					lines := bytes.SplitN(two[1], []byte("\n"), -1)

					// pretty
					var buf bytes.Buffer
					n := len(lines) - 1
					for i := 0; i < n; i++ {
						fmt.Printf("\njson payload line %v:\n", i)
						buf.Reset()
						jsErr := json.Indent(&buf, lines[i], "", "   ")
						if jsErr == nil {
							of.Write(buf.Bytes())
							prettyDone = true
						} else {
							fmt.Printf("\njson err: '%v'\n", jsErr)
						}
					}
				}
				if !prettyDone {
					// ugly
					of.Write(two[1])
				}

				fmt.Printf("\n>>>>>>>>> done writing request %v\n", reqNum)
			} else {
				p.interesting = false
			}
		}
		reqNum++

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}
	p.Director = director
	return p
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func panicOn(err error) {
	if err != nil {
		panic(err)
	}
}

// from reverseproxy.go in http/httputil

// onExitFlushLoop is a callback set by tests to detect the state of the
// flushLoop() goroutine.
var onExitFlushLoop func()

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
type ReverseProxy struct {
	// Director must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	Director func(*http.Request)

	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// FlushInterval specifies the flush interval
	// to flush to the client while copying the
	// response body.
	// If zero, no periodic flushing is done.
	FlushInterval time.Duration

	// ErrorLog specifies an optional logger for errors
	// that occur when attempting to proxy the request.
	// If nil, logging goes to os.Stderr via the log package's
	// standard logger.
	ErrorLog *log.Logger

	// BufferPool optionally specifies a buffer pool to
	// get byte slices for use by io.CopyBuffer when
	// copying HTTP response bodies.
	BufferPool BufferPool

	of io.WriteCloser

	interesting bool
}

// A BufferPool is an interface for getting and returning temporary
// byte slices for use by io.CopyBuffer.
type BufferPool interface {
	Get() []byte
	Put([]byte)
}

// NewSingleHostReverseProxy returns a new ReverseProxy that routes
// URLs to the scheme, host, and base path provided in target. If the
// target's path is "/base" and the incoming request was for "/dir",
// the target request will be for /base/dir.
// NewSingleHostReverseProxy does not rewrite the Host header.
// To rewrite Host headers, use ReverseProxy directly with a custom
// Director policy.
func NewSingleHostReverseProxy(target *url.URL) *ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}
	return &ReverseProxy{Director: director}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",      // canonicalized version of "TE"
	"Trailer", // not Trailers per URL above; http://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

type requestCanceler interface {
	CancelRequest(*http.Request)
}

type runOnFirstRead struct {
	io.Reader // optional; nil means empty body

	fn func() // Run before first Read, then set to nil
}

func (c *runOnFirstRead) Read(bs []byte) (int, error) {
	if c.fn != nil {
		c.fn()
		c.fn = nil
	}
	if c.Reader == nil {
		return 0, io.EOF
	}
	return c.Reader.Read(bs)
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	transport := p.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	outreq := new(http.Request)
	*outreq = *req // includes shallow copies of maps, but okay

	if closeNotifier, ok := rw.(http.CloseNotifier); ok {
		if requestCanceler, ok := transport.(requestCanceler); ok {
			reqDone := make(chan struct{})
			defer close(reqDone)

			clientGone := closeNotifier.CloseNotify()

			outreq.Body = struct {
				io.Reader
				io.Closer
			}{
				Reader: &runOnFirstRead{
					Reader: outreq.Body,
					fn: func() {
						go func() {
							select {
							case <-clientGone:
								requestCanceler.CancelRequest(outreq)
							case <-reqDone:
							}
						}()
					},
				},
				Closer: outreq.Body,
			}
		}
	}

	p.Director(outreq)
	outreq.Proto = "HTTP/1.1"
	outreq.ProtoMajor = 1
	outreq.ProtoMinor = 1
	outreq.Close = false

	// Remove hop-by-hop headers to the backend. Especially
	// important is "Connection" because we want a persistent
	// connection, regardless of what the client sent to us. This
	// is modifying the same underlying map from req (shallow
	// copied above) so we only copy it if necessary.
	copiedHeaders := false
	for _, h := range hopHeaders {
		if outreq.Header.Get(h) != "" {
			if !copiedHeaders {
				outreq.Header = make(http.Header)
				copyHeader(outreq.Header, req.Header)
				copiedHeaders = true
			}
			outreq.Header.Del(h)
		}
	}

	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		// If we aren't the first proxy retain prior
		// X-Forwarded-For information as a comma+space
		// separated list and fold multiple headers into one.
		if prior, ok := outreq.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		outreq.Header.Set("X-Forwarded-For", clientIP)
	}

	res, err := transport.RoundTrip(outreq)
	if err != nil {
		p.logf("http: proxy error: %v", err)
		rw.WriteHeader(http.StatusBadGateway)
		return
	}

	for _, h := range hopHeaders {
		res.Header.Del(h)
	}

	copyHeader(rw.Header(), res.Header)

	// The "Trailer" header isn't included in the Transport's response,
	// at least for *http.Transport. Build it up from Trailer.
	if len(res.Trailer) > 0 {
		var trailerKeys []string
		for k := range res.Trailer {
			trailerKeys = append(trailerKeys, k)
		}
		rw.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
	}

	rw.WriteHeader(res.StatusCode)
	if len(res.Trailer) > 0 {
		// Force chunking if we saw a response trailer.
		// This prevents net/http from calculating the length for short
		// bodies and adding a Content-Length.
		if fl, ok := rw.(http.Flusher); ok {
			fl.Flush()
		}
	}
	p.copyResponse(rw, res.Body)
	res.Body.Close() // close now, instead of defer, to populate res.Trailer
	copyHeader(rw.Header(), res.Trailer)
}

func (p *ReverseProxy) copyResponse(dst io.Writer, src io.Reader) {
	if p.FlushInterval != 0 {
		if wf, ok := dst.(writeFlusher); ok {
			mlw := &maxLatencyWriter{
				dst:     wf,
				latency: p.FlushInterval,
				done:    make(chan bool),
			}
			go mlw.flushLoop()
			defer mlw.stop()
			dst = mlw
		}
	}

	var buf []byte
	if p.BufferPool != nil {
		buf = p.BufferPool.Get()
	}

	if p.interesting {
		fmt.Fprintf(p.of, "\n<<<<<<<<<<<<< Response:")

		var ugly bytes.Buffer
		var pretty bytes.Buffer

		w := io.MultiWriter(&ugly, dst)
		io.CopyBuffer(w, src, buf)

		// prettify and print
		jsErr := json.Indent(&pretty, ugly.Bytes(), "", "   ")
		if jsErr == nil {
			fmt.Fprintf(p.of, " pretty\n")
			p.of.Write(pretty.Bytes())
		} else {
			fmt.Fprintf(p.of, " ugly\n")
			p.of.Write(ugly.Bytes())
		}
	} else {
		io.CopyBuffer(dst, src, buf)
	}

	if p.BufferPool != nil {
		p.BufferPool.Put(buf)
	}
}

func (p *ReverseProxy) logf(format string, args ...interface{}) {
	if p.ErrorLog != nil {
		p.ErrorLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency time.Duration

	mu   sync.Mutex // protects Write + Flush
	done chan bool
}

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dst.Write(p)
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(m.latency)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			if onExitFlushLoop != nil {
				onExitFlushLoop()
			}
			return
		case <-t.C:
			m.mu.Lock()
			m.dst.Flush()
			m.mu.Unlock()
		}
	}
}

func (m *maxLatencyWriter) stop() { m.done <- true }
