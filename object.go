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
	"fmt"
	"io/ioutil"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/urlfetch"
)

const (
	scopeStorageRead  = "https://www.googleapis.com/auth/devstorage.read_only"
	scopeStorageOwner = "https://www.googleapis.com/auth/devstorage.full_control"

	// object custom metadata
	metaRedirect     = "x-goog-meta-redirect"
	metaRedirectCode = "x-goog-meta-redirect-code"
)

var (
	// gcsBase is the GCS base URL for fetching objects.
	// defined as var for easier testing.
	gcsBase = "https://storage.googleapis.com"

	// objectHeaders is a slice of headers propagated from a GCS object.
	objectHeaders = []string{
		"accept-ranges",
		"cache-control",
		"content-disposition",
		"content-encoding",
		"content-md5",
		"content-range",
		"content-type",
		"date",
		"etag",
		"expires",
		"last-modified",
		"retry-after",
		// CORS
		"access-control-allow-origin",
		"access-control-allow-methods",
		"access-control-allow-headers",
		"access-control-allow-credentials",
		"access-control-max-age",
		"access-control-expose-headers",
	}

	// userHeaders are propagated from client to GCS when fetching an object.
	// They must be in canonical form.
	userHeaders = []string{
		"Accept",
		"Accept-Encoding",
		"If-Modified-Since",
		"If-None-Match",
		"If-Range",
		"User-Agent",
		"Range",
	}
)

// object represents a single GCS object.
type object struct {
	Meta map[string]string
	Body []byte
}

func (o *object) redirect() string {
	return o.Meta[metaRedirect]
}

func (o *object) redirectCode() int {
	c, err := strconv.Atoi(o.Meta[metaRedirectCode])
	if err != nil {
		c = http.StatusMovedPermanently
	}
	return c
}

// getFile abstracts getObject and treats object name like a file path.
func getFile(ctx context.Context, bucket, name string) (*object, error) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += defaultIndex
	}

	// try /dir/index.html if name is /dir, concurrently
	var (
		odir    *object
		odirErr error
		donec   = make(chan struct{})
	)
	idxname := path.Join(name, defaultIndex)
	if !strings.HasSuffix(name, defaultIndex) && filepath.Ext(name) == "" {
		go func() {
			odir, odirErr = getObject(ctx, bucket, idxname)
			close(donec)
		}()
	}

	// get the original object meanwhile
	o, err := getObject(ctx, bucket, name)
	if err == nil {
		return o, nil
	}
	// return non-404 errors right away
	if ferr, ok := err.(*fetchError); ok && ferr.code != http.StatusNotFound {
		return nil, err
	}

	// check for /dir/index.html
	<-donec
	if odirErr != nil || odir == nil {
		// it's not a "directory" either; return the original error
		return nil, err
	}
	o = &object{Meta: map[string]string{
		metaRedirect: "/" + idxname[:len(idxname)-len(defaultIndex)],
	}}
	return o, nil
}

// getObject retrieves GCS object obj of the bucket from cache or network.
// Objects fetched from the network are cached before returning
// from this function.
func getObject(ctx context.Context, bucket, obj string) (*object, error) {
	var cacheKey string
	cache := useCache(ctx)
	if cache {
		cacheKey = objectCacheKey(ctx, bucket, obj)
		if o, err := getObjectCache(ctx, cacheKey); err == nil {
			return o, nil
		}
	}
	o, err := fetchObject(ctx, bucket, obj)
	if err != nil {
		return nil, err
	}
	if cache {
		putObjectCache(ctx, cacheKey, o)
	}
	return o, nil
}

// getObjectCache returns an object previously cached with the key.
func getObjectCache(ctx context.Context, key string) (*object, error) {
	o := &object{}
	_, err := memcache.Gob.Get(ctx, key, o)
	if err != nil && err != memcache.ErrCacheMiss {
		log.Errorf(ctx, "memcache.Get(%q): %v", key, err)
	}
	return o, err
}

// putObjectCache updates or creates cached copy of o with the key.
func putObjectCache(ctx context.Context, key string, o *object) error {
	item := memcache.Item{
		Key:        key,
		Object:     o,
		Expiration: 24 * time.Hour,
	}
	err := memcache.Gob.Set(ctx, &item)
	if err != nil {
		log.Errorf(ctx, "memcache.Set(%q): %v", key, err)
	}
	return err
}

// removeObjectCache removes cached object from memcache.
// It returns nil in case where memcache.Delete would result in ErrCacheMiss.
func removeObjectCache(ctx context.Context, bucket, obj string) error {
	keys := make([]string, len(cacheKeyVariants)+1)
	keys[0] = objectCacheKey(context.Background(), bucket, obj)
	for i, v := range cacheKeyVariants {
		keys[i+1] = keys[0] + v
	}
	err := memcache.DeleteMulti(ctx, keys)
	if me, ok := err.(appengine.MultiError); ok {
		for _, e := range me {
			if e != nil && e != memcache.ErrCacheMiss {
				return err
			}
		}
		err = nil
	}
	return err
}

// cacheKeyVariants defines all variants of a cache key of the same object.
// It must be kept in sync with namespace modifiers created by objectCacheKey.
var cacheKeyVariants = []string{":gzip"}

// objectCacheKey returns a cache key for object obj and the given bucket.
//
// Given an object and an in-flight request associated with ctx,
// the key can be different based on some request headers which may modify the response.
func objectCacheKey(ctx context.Context, bucket, obj string) string {
	k := path.Join(bucket, obj)
	if h, ok := ctx.Value(headerKey).(http.Header); ok {
		if strings.Contains(h.Get("accept"), "gzip") {
			k += ":gzip"
		}
	}
	return k
}

// useCache reports whether the in-flight request associated with ctx
// can be responded to with a cached version of an object.
//
// It returns false if either "Range" or any of conditional headers are present
// in the request.
func useCache(ctx context.Context) bool {
	h, ok := ctx.Value(headerKey).(http.Header)
	if !ok {
		return true
	}
	if _, ok := h["Range"]; ok {
		return false
	}
	for k := range h {
		if strings.HasPrefix(k, "If-") {
			return false
		}
	}
	return true
}

// fetchObject retrieves object obj from the given GCS bucket.
// The returned error will be of type fetchError if the storage responds
// with an error code.
func fetchObject(ctx context.Context, bucket, obj string) (*object, error) {
	u := fmt.Sprintf("%s/%s/%s", gcsBase, bucket, obj)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if h, ok := ctx.Value(headerKey).(http.Header); ok {
		addUserHeaders(req, h)
	}
	res, err := httpClient(ctx, scopeStorageOwner).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := ioutil.ReadAll(res.Body)
	if res.StatusCode > 399 {
		err = &fetchError{
			msg:  fmt.Sprintf("%s: %s", res.Status, b),
			code: res.StatusCode,
		}
		return nil, err
	}
	o := &object{
		Body: b,
		Meta: make(map[string]string),
	}
	for _, k := range objectHeaders {
		if v := res.Header.Get(k); v != "" {
			o.Meta[k] = v
		}
	}
	return o, nil
}

// addUserHeaders sets headers on r from h, for all elements of userHeaders.
func addUserHeaders(r *http.Request, h http.Header) {
	for _, k := range userHeaders {
		if v, ok := h[k]; ok {
			r.Header[k] = v
		}
	}
}

// httpClient returns an authenticated http client, suitable for App Engine.
func httpClient(c context.Context, scopes ...string) *http.Client {
	t := &oauth2.Transport{
		Source: google.AppEngineTokenSource(c, scopes...),
		Base:   &urlfetch.Transport{Context: c},
	}
	return &http.Client{Transport: t}
}

type fetchError struct {
	msg  string
	code int
}

func (e *fetchError) Error() string {
	return fmt.Sprintf("fetchError %d: %s", e.code, e.msg)
}
