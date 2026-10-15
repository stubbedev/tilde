package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadsLargerThanTheLimitFail(t *testing.T) {
	for _, c := range []struct {
		size int
		ok   bool
	}{{maxDownload, true}, {maxDownload + 1, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", c.size)))
		}))
		data, err := download(srv.Client(), srv.URL)
		srv.Close()
		if c.ok && (err != nil || len(data) != c.size) || !c.ok && err == nil {
			t.Errorf("download of %d bytes = %d bytes, %v", c.size, len(data), err)
		}
	}
}
