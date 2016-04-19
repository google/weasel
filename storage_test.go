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
	"reflect"
	"strings"
	"testing"

	"google.golang.org/appengine"
	"google.golang.org/appengine/memcache"
)

func TestReadFileIndex(t *testing.T) {
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
	obj, err := stor.ReadFile(ctx, "bucket", "/dir/")
	if err != nil {
		t.Fatalf("stor.ReadFile: %v", err)
	}
	if v := string(obj.Body); v != "test file" {
		t.Errorf("obj.Body = %q; want 'test file'", obj.Body)
	}
}

func TestReadFileNoTrailSlash(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/no/slash/index.html" {
			w.WriteHeader(http.StatusNotFound)
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
	o, err := stor.ReadFile(ctx, "bucket", "/no/slash")
	if err != nil {
		t.Fatalf("stor.ReadFile: %v", err)
	}
	loc := "/no/slash/"
	if v := o.Redirect(); v != loc {
		t.Errorf("o.Redirect() = %q; want %q", v, loc)
	}
	if v := o.RedirectCode(); v != http.StatusMovedPermanently {
		t.Errorf("o.RedirectCode() = %d; want %d", v, http.StatusMovedPermanently)
	}
}

func TestReadObjectCache(t *testing.T) {
	req, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(req)
	stor := &Storage{Base: "invalid"} // make sure we don't hit real GCS
	want := &Object{
		Meta: map[string]string{
			"content-type":  "text/html",
			"cache-control": "public,max-age=10",
		},
		Body: []byte("cached file"),
	}
	item := memcache.Item{
		Key:    stor.CacheKey("bucket", "TestReadObjectCache"),
		Object: want,
	}
	if err := memcache.Gob.Set(ctx, &item); err != nil {
		t.Fatal(err)
	}

	have, err := stor.ReadObject(ctx, "bucket", "TestReadObjectCache")
	if err != nil {
		t.Fatalf("stor.ReadObject: %v", err)
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("have = %+v; want %+v", have, want)
	}
}

func TestReadObjectErr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	req, _ := testInstance.NewRequest("GET", "/", nil)
	ctx := appengine.NewContext(req)
	stor := &Storage{Base: ts.URL}
	obj, err := stor.ReadFile(ctx, "bucket", "TestReadObjectErr")
	if err == nil {
		t.Fatalf("stor.ReadFile: %+v; want error", obj)
	}
	errf, ok := err.(*FetchError)
	if !ok {
		t.Fatalf("want err to be a *FetchError")
	}
	if errf.Code != http.StatusBadRequest {
		t.Errorf("errf.Code = %d; want %d", errf.Code, http.StatusBadRequest)
	}
}
