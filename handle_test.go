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

package weasel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/appengine"
	"google.golang.org/appengine/aetest"
	"google.golang.org/appengine/memcache"
)

func TestValidate_ConfigFile(t *testing.T) {
	var cfg appConfig
	if err := readConfig(&cfg, configFile); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Buckets["default"]; !ok {
		t.Errorf("want default bucket in %+v", cfg)
	}
}

func TestRedirect(t *testing.T) {
	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()

	const (
		redirectTo = "http://www.code-labs.io"
		code       = http.StatusFound
	)
	handler := redirectHandler(redirectTo, code)
	urls := []string{"/", "/page", "/page/", "/page?with=query"}
	for _, u := range urls {
		req, err := ti.NewRequest("GET", u, nil)
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		req.Host = "code-labs.io"
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

func TestServe_DefaultGCS(t *testing.T) {
	const (
		bucket       = "default-bucket"
		reqFile      = "/dir/"
		realFile     = bucket + "/dir/index.html"
		contentType  = "text/plain"
		contents     = "contents"
		cacheControl = "public,max-age=0"
		// dev_appserver app identity stub
		authorization = "Bearer InvalidToken:" + scopeStorageOwner
	)
	// overwrite global config
	config.Buckets = map[string]string{
		"default": bucket,
	}

	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path[1:] != realFile {
			t.Errorf("r.URL.Path = %q; want /%s", r.URL.Path, realFile)
		}
		if v := r.Header.Get("authorization"); !strings.HasPrefix(v, authorization) {
			t.Errorf("auth = %q; want prefix %q", v, authorization)
		}
		if v := r.Header.Get("accept"); v != "client/accept" {
			t.Errorf("accept = %q; want 'client/accept'", v)
		}
		if v, exist := r.Header["X-Foo"]; exist {
			t.Errorf("found x-foo: %q", v)
		}
		w.Header().Set("cache-control", cacheControl)
		w.Header().Set("content-type", contentType)
		w.Header().Set("x-test", "should not propagate")
		w.Write([]byte(contents))
	}))
	defer ts.Close()
	gcsBase = ts.URL

	req, _ := ti.NewRequest("GET", reqFile, nil)
	req.Header.Set("accept", "client/accept")
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

func TestServe_Memcache(t *testing.T) {
	const (
		bucket       = "default-bucket"
		reqFile      = "/index.html"
		realFile     = bucket + "/index.html"
		contentType  = "text/html"
		contents     = "cached file"
		cacheControl = "public,max-age=10"
	)
	// overwrite global config
	config.Buckets = map[string]string{
		"default": bucket,
	}
	obj := &object{
		Meta: map[string]string{
			"content-type":  contentType,
			"cache-control": cacheControl,
		},
		Body: []byte(contents),
	}

	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()

	req, _ := ti.NewRequest("GET", reqFile, nil)
	ctx := appengine.NewContext(req)
	item := memcache.Item{
		Key:    realFile,
		Object: obj,
	}
	if err := memcache.Gob.Set(ctx, &item); err != nil {
		t.Fatal(err)
	}

	gcsBase = "invalid" // make sure we don't hit real GCS
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
	if s := res.Body.String(); s != contents {
		t.Errorf("res.Body = %q; want %q", s, contents)
	}
}

func TestServe_GCSErrors(t *testing.T) {
	const code = http.StatusBadRequest
	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
	defer ts.Close()
	gcsBase = ts.URL

	req, err := ti.NewRequest("GET", "/bad", nil)
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
	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/dir-one/two/index.html" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	gcsBase = ts.URL
	// overwrite global config
	config.Buckets = map[string]string{"default": "bucket"}

	req, _ := ti.NewRequest("GET", "/dir-one/two", nil)
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
	ti, err := aetest.NewInstance(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()

	body := `{"bucket": "dummy", "name": "path/obj"}`
	req, _ := ti.NewRequest("POST", "/-/hook/gcs", strings.NewReader(body))

	ctx := appengine.NewContext(req)
	key := objectCacheKey(ctx, "dummy", "path/obj")
	item := &memcache.Item{Key: key, Value: []byte("ignored")}
	if err := memcache.Set(ctx, item); err != nil {
		t.Fatal(err)
	}

	// must remove cached item
	res := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
	if _, err := memcache.Get(ctx, key); err != memcache.ErrCacheMiss {
		t.Fatalf("memcache.Get(%q): %v; want ErrCacheMiss", key, err)
	}

	// cache misses must not respond with an error code
	req, _ = ti.NewRequest("POST", "/-/hook/gcs", strings.NewReader(body))
	res = httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
}
