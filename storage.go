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

// Package weasel provides means for serving content from a Google Cloud Storage (GCS)
// bucket, suitable for hosting on Google App Engine.
// See README.md for the design details.
//
// This package is a work in progress and makes no API stability promises.
package weasel

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pehowell/weasel/internal"

	"golang.org/x/oauth2"
	"google.golang.org/appengine/v2/log"
	"google.golang.org/appengine/v2/memcache"
	"google.golang.org/appengine/v2/urlfetch"
)

// Google Cloud Storage OAuth2 scopes.
const scopeStorageRead = "https://www.googleapis.com/auth/devstorage.read_only"

// DefaultStorage is a Storage with sensible default parameters.
var DefaultStorage = &Storage{
	Base:  "https://storage.googleapis.com",
	Index: "index.html",
	CORS: CORS{
		Origin: []string{"*"},
		MaxAge: "86400",
	},
}

// CORS is a Storage cross-origin settings.
type CORS struct {
	Origin []string // allowed origins
	MaxAge string   // preflight cache, in seconds
}

// Storage incapsulates configuration params for retrieveing and serving GCS objects.
type Storage struct {
	Base  string // GCS service base URL, e.g. "https://storage.googleapis.com".
	Index string // Appended to an object name in certain cases, e.g. "index.html".
	CORS  CORS
}

// OpenFile abstracts Open and treats object name like a file path.
func (s *Storage) OpenFile(ctx context.Context, bucket, name string) (*Object, error) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += s.Index
	}

	// stat /dir/index.html if name is /dir, concurrently
	checkStat := !strings.HasSuffix(name, s.Index) && filepath.Ext(name) == ""
	type stat struct {
		o   *Object
		err error
	}
	var ch chan *stat
	if checkStat {
		ch = make(chan *stat, 1)
		go func() {
			o, err := s.Stat(ctx, bucket, path.Join(name, s.Index))
			ch <- &stat{o, err}
			close(ch)
		}()
	}

	// try the original object meanwhile
	o, err := s.Open(ctx, bucket, name)
	if err == nil || !checkStat {
		return o, err
	}
	// Return non-404 errors right away, even when checkStat == true.
	// Note that GCS now may respond with 403 Forbidden
	// for nonexistent objects.
	if ferr, ok := err.(*FetchError); ok && ferr.Code != 404 && ferr.Code != 403 {
		return nil, err
	}

	// wait some time for stat obj
	// TODO: use ctxhttp
	select {
	case <-time.After(5 * time.Second):
		log.Errorf(ctx, "s.Stat(bucket=%q) timeout", bucket)
		// return original Open error
		return nil, err
	case res := <-ch:
		if res.err != nil {
			// return original Open error
			return nil, err
		}
		o = res.o
	}
	if o.Redirect() == "" {
		o = &Object{
			Body: ioutil.NopCloser(bytes.NewReader(nil)),
			Meta: map[string]string{
				metaRedirect: path.Join("/", name) + "/",
			},
		}
	}
	return o, nil
}

// Open retrieves GCS object name of the bucket from cache or network.
// Objects fetched from the network are cached before returning
// from this function.
func (s *Storage) Open(ctx context.Context, bucket, name string) (*Object, error) {
	key := s.CacheKey(bucket, name)
	o, err := getCache(ctx, key)
	if err != nil {
		u := fmt.Sprintf("%s/%s", s.Base, path.Join(bucket, name))
		o, err = fetch(ctx, u, key)
	}
	return o, err
}

// Stat is similar to Read except the returned Object.Body may be nil.
// In the case where Body is not nil, calling Body.Close() is not required.
func (s *Storage) Stat(ctx context.Context, bucket, name string) (*Object, error) {
	if o, err := getCache(ctx, s.CacheKey(bucket, name)); err == nil {
		return o, nil
	}
	u := fmt.Sprintf("%s/%s", s.Base, path.Join(bucket, name))
	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient(ctx, scopeStorageRead).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(res.Body)
		return nil, &FetchError{
			Msg:  fmt.Sprintf("%s: %s", res.Status, b),
			Code: res.StatusCode,
		}
	}
	meta := make(map[string]string)
	for _, k := range objectHeaders {
		if v := res.Header.Get(k); v != "" {
			meta[k] = v
		}
	}
	return &Object{Meta: meta}, nil
}

// PurgeCache removes cached object from memcache.
// It does not return an error in the case of cache miss.
func (s *Storage) PurgeCache(ctx context.Context, bucket, name string) error {
	return purgeCache(ctx, s.CacheKey(bucket, name))
}

// CacheKey returns a key to cache an object under, computed from
// s.Base, bucket and then name.
func (s *Storage) CacheKey(bucket, name string) string {
	return fmt.Sprintf("%s/%s", s.Base, path.Join(bucket, name))
}

// fetch retrieves object from the given url.
// The returned error will be of type FetchError if the storage responds
// with an error code.
//
// The returned Object.Body will auto-cache in memcache if cacheKey
// is provided and body length is within allowed cache limits.
func fetch(ctx context.Context, url, cacheKey string) (*Object, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient(ctx, scopeStorageRead).Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode > 399 {
		// FetchError takes precedence over i/o errors
		b, _ := ioutil.ReadAll(res.Body)
		res.Body.Close()
		return nil, &FetchError{
			Msg:  fmt.Sprintf("%s: %s", res.Status, b),
			Code: res.StatusCode,
		}
	}
	m := make(map[string]string)
	for _, k := range objectHeaders {
		if v := res.Header.Get(k); v != "" {
			m[k] = v
		}
	}
	rc := res.Body
	if cacheKey != "" && res.ContentLength < cacheItemMax {
		rc = &objectBuf{
			Meta: m,
			r:    res.Body,
			key:  cacheKey,
			ctx:  ctx,
		}
	}
	o := &Object{
		Meta: m,
		Body: rc,
	}
	return o, nil
}

func getCache(ctx context.Context, key string) (*Object, error) {
	var b objectBuf
	if _, err := memcache.Gob.Get(ctx, key, &b); err != nil {
		if err != memcache.ErrCacheMiss {
			log.Errorf(ctx, "memcache.Gob.Get(%q): %v", key, err)
		}
		return nil, err
	}
	o := &Object{
		Meta: b.Meta,
		Body: ioutil.NopCloser(bytes.NewReader(b.Body)),
	}
	return o, nil
}

func purgeCache(ctx context.Context, key string) error {
	err := memcache.Delete(ctx, key)
	if err == memcache.ErrCacheMiss {
		err = nil
	}
	return err
}

func httpClient(ctx context.Context, scopes ...string) *http.Client {
	t := &oauth2.Transport{
		Source: internal.AETokenSource(ctx, scopes...),
		Base:   &urlfetch.Transport{Context: ctx},
	}
	return &http.Client{Transport: t}
}

// FetchError contains error code and message from a GCS response.
type FetchError struct {
	Msg  string
	Code int
}

// Error returns formatted FetchError.
func (e *FetchError) Error() string {
	return fmt.Sprintf("FetchError %d: %s", e.Code, e.Msg)
}
