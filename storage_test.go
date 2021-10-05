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
	"reflect"
	"strings"
	"testing"

	"google.golang.org/appengine/v2"
	"google.golang.org/appengine/v2/memcache"
)

func TestOpenFileIndex(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// dev_appserver app identity stub
		auth := "Bearer InvalidToken:https://www.googleapis.com/auth/devstorage.read_only"
		if v := r.Header.Get("authorization"); !strings.HasPrefix(v, auth) {
			t.Errorf("auth = %q; want prefix %q", v, auth)
		}
		if r.URL.Path != "/bucket/dir/index" {
			t.Errorf("r.URL.Path = %q; want /bucket/dir/index", r.URL.Path)
		}
		// weasel client => GCS always uses gzip where available
		if v := r.Header.Get("accept-encoding"); v != "gzip" {
			t.Errorf("accept-encoding = %q; want 'gzip'", v)
		}
		w.Header().Set("content-type", "text/plain")
		w.Write([]byte("test file"))
	}))
	defer ts.Close()

	req, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(req)
	// make sure we're not getting memcached results
	if err := memcache.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	stor := &Storage{Base: ts.URL, Index: "index"}
	obj, err := stor.OpenFile(ctx, "bucket", "/dir/")
	if err != nil {
		t.Fatalf("stor.OpenFile: %v", err)
	}
	defer obj.Body.Close()
	b, _ := ioutil.ReadAll(obj.Body)
	if string(b) != "test file" {
		t.Errorf("obj.Body = %q; want 'test file'", b)
	}
}

func TestOpenFileNoTrailSlash_404(t *testing.T) {
	testOpenFileNoTrailSlash(t, http.StatusNotFound)
}

func TestOpenFileNoTrailSlash_403(t *testing.T) {
	testOpenFileNoTrailSlash(t, http.StatusForbidden)
}

func testOpenFileNoTrailSlash(t *testing.T, status int) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/no/slash/index.html" {
			w.WriteHeader(status)
			return
		}
		// stat request
		if r.Method != "HEAD" {
			t.Errorf("r.Method = %q; want HEAD", r.Method)
		}
	}))
	defer ts.Close()

	r, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(r)
	// make sure we're not getting memcached results
	if err := memcache.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	stor := &Storage{Base: ts.URL, Index: "index.html"}
	o, err := stor.OpenFile(ctx, "bucket", "/no/slash")
	if err != nil {
		t.Fatalf("stor.OpenFile: %v", err)
	}
	defer o.Body.Close()
	loc := "/no/slash/"
	if v := o.Redirect(); v != loc {
		t.Errorf("o.Redirect() = %q; want %q", v, loc)
	}
	if v := o.RedirectCode(); v != http.StatusMovedPermanently {
		t.Errorf("o.RedirectCode() = %d; want %d", v, http.StatusMovedPermanently)
	}
}

func TestOpenAndCache(t *testing.T) {
	const body = `{"foo":"bar"}`
	meta := map[string]string{"content-type": "application/json"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range meta {
			w.Header().Set(k, v)
		}
		w.Write([]byte(body))
	}))
	defer ts.Close()

	r, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(r)
	// make sure we're not getting memcached results
	if err := memcache.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	stor := &Storage{Base: ts.URL}
	o, err := stor.Open(ctx, "bucket", "/file.json")
	if err != nil {
		t.Fatalf("stor.Open: %v", err)
	}
	defer o.Body.Close()
	b, err := ioutil.ReadAll(o.Body)
	if err != nil {
		t.Fatalf("ReadAll(o.Body): %v", err)
	}
	if string(b) != body {
		t.Errorf("o.Body = %q; want %q", b, body)
	}
	if !reflect.DeepEqual(o.Meta, meta) {
		t.Errorf("o.Meta = %+v; want %+v", o.Meta, meta)
	}

	key := stor.CacheKey("bucket", "/file.json")
	var ob objectBuf
	if _, err := memcache.Gob.Get(ctx, key, &ob); err != nil {
		t.Fatalf("memcache.Gob.Get(%q): %v", key, err)
	}
	if string(ob.Body) != body {
		t.Errorf("ob.Body = %q; want %q", ob.Body, body)
	}
	if !reflect.DeepEqual(ob.Meta, meta) {
		t.Errorf("ob.Meta = %+v; want %+v", ob.Meta, meta)
	}
}

func TestOpenFromCache(t *testing.T) {
	r, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(r)
	stor := &Storage{Base: "invalid"} // make sure we don't hit real GCS
	ob := &objectBuf{
		Meta: map[string]string{
			"content-type":  "text/html",
			"cache-control": "public,max-age=10",
		},
		Body: []byte("cached file"),
	}
	item := memcache.Item{
		Key:    stor.CacheKey("bucket", "TestOpenFromCache"),
		Object: ob,
	}
	if err := memcache.Gob.Set(ctx, &item); err != nil {
		t.Fatal(err)
	}

	o, err := stor.Open(ctx, "bucket", "TestOpenFromCache")
	if err != nil {
		t.Fatalf("stor.Open: %v", err)
	}
	defer o.Body.Close()
	if _, isbuf := o.Body.(*objectBuf); isbuf {
		t.Errorf("o.Body is *objectBuf")
	}
	if !reflect.DeepEqual(o.Meta, ob.Meta) {
		t.Errorf("o.Meta = %+v; want %+v", o.Meta, ob.Meta)
	}
	b, _ := ioutil.ReadAll(o.Body)
	if string(b) != "cached file" {
		t.Errorf("o.Body = %q; want 'cached file'", b)
	}
}

func TestOpenErr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	req, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(req)
	stor := &Storage{Base: ts.URL}
	obj, err := stor.OpenFile(ctx, "bucket", "TestOpenErr")
	if err == nil {
		defer obj.Body.Close()
		t.Fatalf("stor.OpenFile: %+v; want error", obj)
	}
	errf, ok := err.(*FetchError)
	if !ok {
		t.Fatalf("want err to be a *FetchError")
	}
	if errf.Code != http.StatusBadRequest {
		t.Errorf("errf.Code = %d; want %d", errf.Code, http.StatusBadRequest)
	}
}
