// Minimal HTTP client used by the HTTP tool modal in the UI.
//
// Routes through an active SOCKS5 forward when SocksAddr is set -
// useful for hitting endpoints inside the remote network without
// curl gymnastics. Without it, requests go through the host's
// default network (same as fetch would).
//
// Body is sent verbatim; the caller decides Content-Type. Response
// body is read up to a soft cap to keep the IPC payload sane; the
// rest is dropped with a marker so the UI can show "(truncated)".

package httpc

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const (
	defaultTimeout = 60 * time.Second
	bodyReadCap    = 4 * 1024 * 1024 // 4 MiB
)

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Request struct {
	Method        string   `json:"method"`
	URL           string   `json:"url"`
	Headers       []Header `json:"headers"`
	Body          string   `json:"body"`
	TLSSkipVerify bool     `json:"tls_skip_verify"`
	// SocksAddr routes the request through this SOCKS5 proxy (host:port).
	// Empty = direct.
	SocksAddr string `json:"socks_addr"`
	// TimeoutSeconds defaults to 60 if 0.
	TimeoutSeconds int `json:"timeout_seconds"`
}

type Response struct {
	Status     string   `json:"status"`
	StatusCode int      `json:"status_code"`
	Headers    []Header `json:"headers"`
	Body       string   `json:"body"`
	Truncated  bool     `json:"truncated"`
	DurationMs int64    `json:"duration_ms"`
}

func Do(req Request) (*Response, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if req.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if _, err := url.Parse(req.URL); err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: req.TLSSkipVerify},
		// Disable keep-alive to keep each request self-contained; we
		// don't reuse the client across calls.
		DisableKeepAlives: true,
	}
	if req.SocksAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", req.SocksAddr, nil, &net.Dialer{Timeout: timeout})
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		// Some net/http versions only honor DialContext; wire both.
		transport.Dial = dialer.Dial
		if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = ctxDialer.DialContext
		}
	}

	client := &http.Client{Transport: transport, Timeout: timeout}

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	httpReq, err := http.NewRequest(method, req.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for _, h := range req.Headers {
		if strings.TrimSpace(h.Name) == "" {
			continue
		}
		// Use Add (not Set) so multiple values for the same header work.
		httpReq.Header.Add(h.Name, h.Value)
	}

	t0 := time.Now()
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	limited := io.LimitReader(httpResp.Body, bodyReadCap+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	truncated := false
	if len(bodyBytes) > bodyReadCap {
		bodyBytes = bodyBytes[:bodyReadCap]
		truncated = true
	}

	// Flatten the response header map back into a list so duplicates
	// survive the round-trip.
	var outHeaders []Header
	for name, values := range httpResp.Header {
		for _, v := range values {
			outHeaders = append(outHeaders, Header{Name: name, Value: v})
		}
	}

	return &Response{
		Status:     httpResp.Status,
		StatusCode: httpResp.StatusCode,
		Headers:    outHeaders,
		Body:       string(bodyBytes),
		Truncated:  truncated,
		DurationMs: time.Since(t0).Milliseconds(),
	}, nil
}
