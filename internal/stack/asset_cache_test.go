package stack

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The browser keeps app.js and app.css, asks each time with If-None-Match, and
// gets an empty 304 answer while the embedded file is the same.
func TestBrowserAssetsRevalidateWithETag(t *testing.T) {
	f := tasksFixture(t)
	webClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	get := func(path, tag string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", f.service.Discovery.URL+path, nil)
		if tag != "" {
			req.Header.Set("If-None-Match", tag)
		}
		r, err := webClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, b
	}
	for _, path := range []string{"/assets/app.js", "/assets/app.css"} {
		first, body := get(path, "")
		tag := first.Header.Get("ETag")
		if first.StatusCode != 200 || len(body) == 0 || len(tag) != 26 || first.Header.Get("Cache-Control") != "no-cache" || first.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: status %d, ETag %q, Cache-Control %q", path, first.StatusCode, tag, first.Header.Get("Cache-Control"))
		}
		again, body := get(path, tag)
		if again.StatusCode != http.StatusNotModified || len(body) != 0 || again.Header.Get("ETag") != tag {
			t.Fatalf("%s: revalidation got status %d with %d bytes", path, again.StatusCode, len(body))
		}
		changed, body := get(path, `"0000"`)
		if changed.StatusCode != 200 || len(body) == 0 {
			t.Fatalf("%s: a stale ETag got status %d", path, changed.StatusCode)
		}
	}
	// The page itself is never stored: it names this service instance.
	u, _ := viewTest(t, f, f.ref(0))
	page, _ := get(u.Path, "")
	if page.Header.Get("Cache-Control") != "no-store" || page.Header.Get("ETag") != "" {
		t.Fatal("the page must stay no-store")
	}
	if page.Header.Get("X-A-Stack-Assets") != browserAssets || browserAssets == strings.Trim(browserAssetTags["browser/app.js"], `"`) {
		t.Fatal("the page must name both view files together")
	}
}
