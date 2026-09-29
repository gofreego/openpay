package http_server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestConsoleHandler(t *testing.T) {
	h := ConsoleHandler(fstest.MapFS{
		"index.html":        {Data: []byte("<html>console</html>")},
		"assets/app-abc.js": {Data: []byte("console.log(1)")},
	})

	if rec := serve(t, h, "/payments/assets/app-abc.js"); rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q cache %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	// A client-side route is not a file: it gets the app, uncached.
	for _, path := range []string{"/payments/", "/payments/dashboard", "/payments/wallets/wlt_1"} {
		rec := serve(t, h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console") || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q cache %q", path, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	if rec := serve(t, h, "/payments"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/payments/" {
		t.Errorf("/payments: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A binary built without the console says so rather than serving a blank page.
func TestConsoleHandlerWithoutABuild(t *testing.T) {
	h := ConsoleHandler(fstest.MapFS{".gitkeep": {}})
	if rec := serve(t, h, "/payments/"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make ui") {
		t.Errorf("unbuilt console: %d %q", rec.Code, rec.Body.String())
	}
}
