package drive

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// transport is shared by all Drive and token requests. The defaults keep idle
// connections for 90 s, longer than routers and NAT keep the mapping alive,
// so the next poll (every 60 s) often wrote into a dead connection and hung
// until the OS gave up. These timeouts drop such connections quickly instead.
var transport = newTransport()

func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.IdleConnTimeout = 30 * time.Second
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 45 * time.Second
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	// Google serves HTTP/2: ping a connection that has been silent for 15 s
	// and close it when the ping is not answered (also covers stalled
	// downloads, which ResponseHeaderTimeout does not).
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 15 * time.Second, PingTimeout: 10 * time.Second}
	return t
}

// withTransport makes oauth2 use the shared transport.
func withTransport(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: transport})
}

// retryNetwork reports whether a request that failed below HTTP may be sent
// again: only on a broken connection, never when the caller gave up, and not
// for creates (a lost response may hide a created file).
func retryNetwork(req *http.Request, err error) bool {
	if req.Context().Err() != nil || errors.Is(err, ErrReauth) {
		return false
	}
	if req.Body != nil && req.GetBody == nil {
		return false
	}
	return req.Method != http.MethodPost || req.URL.Query().Get("uploadType") == "resumable"
}
