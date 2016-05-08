// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/appengine"
	"google.golang.org/appengine/memcache"
)

func TestServerConfig(t *testing.T) {
	if b := config.Buckets["default"]; b == "" {
		t.Errorf("want default bucket in %+v", config)
	}
}

func TestRedirect(t *testing.T) {
	const (
		redirectTo = "https://www.example.com"
		code       = http.StatusFound
	)
	handler := redirectHandler(redirectTo, code)
	urls := []string{"/", "/page", "/page/", "/page?with=query"}
	for _, u := range urls {
		req, err := testInstance.NewRequest("GET", u, nil)
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		req.Host = "example.org"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != code {
			t.Errorf("%s: res.Code = %d; want %d", u, res.Code, code)
		}
		redir := redirectTo + u
		if v := res.Header().Get("location"); v != redir {
			t.Errorf("%s: location = %q; want %q", u, v, redir)
		}
	}
}

func TestTLSOnly(t *testing.T) {
	config.tlsOnly = map[string]struct{}{"example.com": {}}
	r, _ := testInstance.NewRequest("GET", "http://example.com/page?foo=bar", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, r)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusMovedPermanently)
	}
	want := "https://example.com/page?foo=bar"
	if l := w.Header().Get("location"); l != want {
		t.Errorf("location = %q; want %q", l, want)
	}
}

func TestServe_DefaultGCS(t *testing.T) {
	const (
		bucket       = "default-bucket"
		reqFile      = "/dir/"
		realFile     = bucket + "/dir/index.html"
		contents     = "contents"
		contentType  = "text/plain"
		cacheControl = "public,max-age=0"
		// dev_appserver app identity stub
		authorization = "Bearer InvalidToken:https://www.googleapis.com/auth/devstorage.read_only"
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path[1:] != realFile {
			t.Errorf("r.URL.Path = %q; want /%s", r.URL.Path, realFile)
		}
		if v := r.Header.Get("authorization"); !strings.HasPrefix(v, authorization) {
			t.Errorf("auth = %q; want prefix %q", v, authorization)
		}
		if v, exist := r.Header["X-Foo"]; exist {
			t.Errorf("found x-foo: %q", v)
		}
		// weasel client => GCS always uses gzip where available
		if v := r.Header.Get("accept-encoding"); v != "gzip" {
			t.Errorf("accept-encoding = %q; want 'gzip'", v)
		}
		w.Header().Set("cache-control", cacheControl)
		w.Header().Set("content-type", contentType)
		w.Header().Set("x-test", "should not propagate")
		w.Write([]byte(contents))
	}))
	defer ts.Close()
	storage.Base = ts.URL
	config.Buckets = map[string]string{"default": bucket}

	req, _ := testInstance.NewRequest("GET", reqFile, nil)
	req.Header.Set("accept-encoding", "client/accept")
	req.Header.Set("x-foo", "bar")
	// make sure we're not getting memcached results
	if err := memcache.Flush(appengine.NewContext(req)); err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
	if v := res.Header().Get("cache-control"); v != cacheControl {
		t.Errorf("cache-control = %q; want %q", v, cacheControl)
	}
	if v := res.Header().Get("content-type"); v != contentType {
		t.Errorf("content-type = %q; want %q", v, contentType)
	}
	if v := res.Header().Get("x-test"); v != "" {
		t.Errorf("found x-test header: %q", v)
	}
	if s := res.Body.String(); s != contents {
		t.Errorf("res.Body = %q; want %q", s, contents)
	}
}

func TestServe_Methods(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain")
		w.Write([]byte("methods test"))
	}))
	defer ts.Close()
	storage.Base = ts.URL
	config.Buckets = map[string]string{"default": "bucket"}

	tests := []struct {
		method, body string
		code         int
	}{
		{"HEAD", "", http.StatusOK},
		{"OPTIONS", "", http.StatusOK},
		// it is important that GET comes last to verify requests like HEAD
		// do not corrupt object cache
		{"GET", "methods test", http.StatusOK},

		{"PUT", "", http.StatusMethodNotAllowed},
		{"POST", "", http.StatusMethodNotAllowed},
		{"DELETE", "", http.StatusMethodNotAllowed},
	}
	for i, test := range tests {
		r, _ := testInstance.NewRequest(test.method, "/file.txt", nil)
		rw := httptest.NewRecorder()
		http.DefaultServeMux.ServeHTTP(rw, r)
		if rw.Code != test.code {
			t.Errorf("%d: rw.Code = %d; want %d", i, rw.Code, test.code)
		}
		if v := strings.TrimSpace(rw.Body.String()); v != test.body {
			t.Errorf("%d: rw.Body = %q; want %q", i, v, test.body)
		}
	}
}

func TestServe_GCSErrors(t *testing.T) {
	const code = http.StatusBadRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
	defer ts.Close()
	storage.Base = ts.URL

	req, err := testInstance.NewRequest("GET", "/bad", nil)
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != code {
		t.Errorf("res.Code = %d; want %d", res.Code, code)
	}
}

func TestServe_NoTrailSlash(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/dir-one/two/index.html" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// stat request
		if r.Method != "HEAD" {
			t.Errorf("r.Method = %q; want HEAD", r.Method)
		}
	}))
	defer ts.Close()
	storage.Base = ts.URL
	config.Buckets = map[string]string{"default": "bucket"}

	req, _ := testInstance.NewRequest("GET", "/dir-one/two", nil)
	// make sure we're not getting memcached results
	if err := memcache.Flush(appengine.NewContext(req)); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusMovedPermanently {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusMovedPermanently)
	}
	loc := "/dir-one/two/"
	if v := res.Header().Get("location"); v != loc {
		t.Errorf("location = %q; want %q", v, loc)
	}
}

func TestHook(t *testing.T) {
	body := `{"bucket": "dummy", "name": "path/obj"}`
	cacheKey := storage.CacheKey("dummy", "path/obj")
	req, _ := testInstance.NewRequest("POST", "/-/hook/gcs", strings.NewReader(body))
	ctx := appengine.NewContext(req)
	item := &memcache.Item{Key: cacheKey, Value: []byte("ignored")}
	if err := memcache.Set(ctx, item); err != nil {
		t.Fatal(err)
	}

	// must remove cached item
	res := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
	if _, err := memcache.Get(ctx, cacheKey); err != memcache.ErrCacheMiss {
		t.Fatalf("memcache.Get(%q): %v; want ErrCacheMiss", cacheKey, err)
	}

	// cache misses must not respond with an error code
	req, _ = testInstance.NewRequest("POST", "/-/hook/gcs", strings.NewReader(body))
	res = httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
}
