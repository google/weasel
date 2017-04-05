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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/appengine"
	"google.golang.org/appengine/memcache"
)

func TestServeRedirect(t *testing.T) {
	const redir = "https://example.com"
	stor := &Storage{}
	o := &Object{
		Meta: map[string]string{
			metaRedirect:     redir,
			metaRedirectCode: "302",
		},
	}
	r, _ := http.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	err := stor.ServeObject(w, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusFound {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusFound)
	}
	if v := w.Header().Get("location"); v != redir {
		t.Errorf("location = %q; want %q", v, redir)
	}
}

func TestServeCross(t *testing.T) {
	stor := &Storage{
		CORS: CORS{
			Origin: []string{"*"},
			MaxAge: "300",
		},
	}
	o := &Object{Body: ioutil.NopCloser(strings.NewReader("test"))}
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("origin", "https://example.com")
	w := httptest.NewRecorder()

	err := stor.ServeObject(w, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusOK)
	}
	if v := w.Body.String(); v != "test" {
		t.Errorf("didn't want w.Body: %q", v)
	}
	if v := w.Header().Get("access-control-allow-origin"); v != "*" {
		t.Errorf("allow-origin: %q; want *", v)
	}
}

func TestServeCrossPreflight(t *testing.T) {
	stor := &Storage{
		CORS: CORS{
			Origin: []string{"*"},
			MaxAge: "300",
		},
	}
	o := &Object{
		Meta: map[string]string{
			metaRedirect:     "https://example.org",
			metaRedirectCode: "302",
		},
		Body: ioutil.NopCloser(strings.NewReader("test")),
	}
	r, _ := http.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("origin", "https://example.com")
	r.Header.Set("access-control-request-method", "GET")
	r.Header.Set("access-control-request-headers", "X-Foo")
	w := httptest.NewRecorder()

	err := stor.ServeObject(w, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusOK)
	}
	if v := w.Body.String(); v != "" {
		t.Errorf("didn't want w.Body: %q", v)
	}
	if v := w.Header().Get("access-control-allow-origin"); v != "*" {
		t.Errorf("allow-origin: %q; want *", v)
	}
	if v := w.Header().Get("access-control-allow-headers"); v != "X-Foo" {
		t.Errorf("allow-headers: %q; want X-Foo", v)
	}
	if v := w.Header().Get("access-control-max-age"); v != "300" {
		t.Errorf("max-age: %q; want 300", v)
	}
	want := "GET, HEAD, OPTIONS"
	if v := w.Header().Get("access-control-allow-methods"); v != want {
		t.Errorf("allow-methods: %q; want %q", v, want)
	}
}

func TestHook(t *testing.T) {
	var stor Storage
	r, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(r)
	cacheKey := stor.CacheKey("dummy", "path/obj")
	item := &memcache.Item{Key: cacheKey, Value: []byte("ignored")}
	if err := memcache.Set(ctx, item); err != nil {
		t.Fatal(err)
	}

	body := `{"bucket": "dummy", "name": "path/obj"}`
	req, _ := testInstance.NewRequest("POST", "/hook", strings.NewReader(body))
	res := httptest.NewRecorder()
	stor.HandleChangeHook(res, req)
	if res.Code != http.StatusOK {
		t.Errorf("res.Code = %d; want %d", res.Code, http.StatusOK)
	}
	// Must remove cached item.
	if _, err := memcache.Get(ctx, cacheKey); err != memcache.ErrCacheMiss {
		t.Fatalf("memcache.Get(%q): %v; want ErrCacheMiss", cacheKey, err)
	}
}
