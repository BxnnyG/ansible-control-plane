package server

import (
	"net/http"
	"testing"
	"testing/fstest"
)

func TestStaticCacheHeaders(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":                  {Data: []byte("<!doctype html><title>x</title>")},
		"_app/immutable/entry/app.js": {Data: []byte("console.log(1)")},
		"favicon.svg":                 {Data: []byte("<svg/>")},
	}
	h := Router(quiet, fakePing{}, fsys)
	tests := []struct {
		path, want string
	}{
		{"/_app/immutable/entry/app.js", "public, max-age=31536000, immutable"},
		{"/", "no-cache"},
		{"/templates/123", "no-cache"}, // SPA fallback
		{"/favicon.svg", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(h, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}
