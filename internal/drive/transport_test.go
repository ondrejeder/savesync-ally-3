package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestRetryNetwork(t *testing.T) {
	netErr := errors.New("wsarecv: connection timed out")
	req := func(method, target string, body io.Reader) *http.Request {
		r, _ := http.NewRequest(method, target, body)
		return r
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"get", req(http.MethodGet, apiBase+"/changes", nil), true},
		{"patch with replayable body", req(http.MethodPatch, apiBase+"/files/x", bytes.NewReader([]byte("{}"))), true},
		{"resumable session", req(http.MethodPost, uploadBase+"/files?uploadType=resumable", bytes.NewReader([]byte("{}"))), true},
		{"create folder", req(http.MethodPost, apiBase+"/files", bytes.NewReader([]byte("{}"))), false},
		{"streamed body", req(http.MethodPut, uploadBase+"/session", io.NopCloser(bytes.NewReader(nil))), false},
		{"caller gave up", req(http.MethodGet, apiBase+"/changes", nil).WithContext(cancelled), false},
	}
	for _, c := range cases {
		if got := retryNetwork(c.req, netErr); got != c.want {
			t.Errorf("%s: retryNetwork = %v, want %v", c.name, got, c.want)
		}
	}
	if retryNetwork(req(http.MethodGet, apiBase+"/changes", nil), ErrReauth) {
		t.Error("ErrReauth must not be retried")
	}
}
