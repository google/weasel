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

	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/urlfetch"
)

const (
	// defaultIndex is the trailing part of GCS object name
	// when none is specified in an in-flight request.
	defaultIndex = "index.html"

	// object custom metadata
	metaRedirect     = "x-goog-meta-redirect"
	metaRedirectCode = "x-goog-meta-redirect-code"

	// GCS OAuth2 scopes
	scopeStorageRead  = "https://www.googleapis.com/auth/devstorage.read_only"
	scopeStorageOwner = "https://www.googleapis.com/auth/devstorage.full_control"
)

var (
	// gcsBase is the GCS base URL for fetching objects.
	// defined as var for easier testing.
	gcsBase = "https://storage.googleapis.com"

	// objectHeaders is a slice of headers propagated from a GCS object.
	objectHeaders = []string{
		"cache-control",
		"content-disposition",
		"content-md5",
		"content-type",
		"etag",
		"last-modified",
		// CORS
		"access-control-allow-methods",
		"access-control-allow-origin",
		"access-control-allow-headers",
		"access-control-allow-credentials",
		"access-control-max-age",
		"access-control-expose-headers",
	}

	// userHeaders are propagated from client to GCS when fetching an object.
	// They must be in canonical form.
	userHeaders = []string{
		"If-Modified-Since",
		"If-None-Match",
		"Origin",
		"User-Agent",
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

	// stat /dir/index.html if name is /dir, concurrently
	var (
		stat    *object
		statErr error
		donec   = make(chan struct{})
	)
	idxname := path.Join(name, defaultIndex)
	if !strings.HasSuffix(name, defaultIndex) && filepath.Ext(name) == "" {
		go func() {
			stat, statErr = statObject(ctx, bucket, idxname)
			close(donec)
		}()
	} else {
		close(donec)
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

	// wait for stat obj
	select {
	case <-time.After(5 * time.Second):
		log.Errorf(ctx, "statObject(%q, %q): timeout", bucket, idxname)
	case <-donec:
		// done stat-ing obj
	}
	if statErr != nil || stat == nil {
		// it's not a "directory" either; return the original error
		return nil, err
	}
	if stat.redirect() == "" {
		stat = &object{Meta: map[string]string{
			metaRedirect: "/" + idxname[:len(idxname)-len(defaultIndex)],
		}}
	}
	return stat, nil
}

// getObject retrieves GCS object obj of the bucket from cache or network.
// Objects fetched from the network are cached before returning
// from this function.
func getObject(ctx context.Context, bucket, obj string) (*object, error) {
	key := path.Join(bucket, obj)
	cache := useCache(ctx)
	if cache {
		if o, err := getObjectCache(ctx, key); err == nil {
			return o, nil
		}
	}
	o, err := fetchObject(ctx, bucket, obj)
	if err != nil {
		return nil, err
	}
	if cache {
		putObjectCache(ctx, key, o)
	}
	return o, nil
}

// statObject is similar to fetchObject except the returned object.Body
// may be nil.
func statObject(ctx context.Context, bucket, obj string) (*object, error) {
	if o, err := getObjectCache(ctx, path.Join(bucket, obj)); err == nil {
		return o, nil
	}
	u := fmt.Sprintf("%s/%s", gcsBase, path.Join(bucket, obj))
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
		return nil, &fetchError{
			msg:  fmt.Sprintf("%s: %s", res.Status, b),
			code: res.StatusCode,
		}
	}
	meta := make(map[string]string)
	for _, k := range objectHeaders {
		if v := res.Header.Get(k); v != "" {
			meta[k] = v
		}
	}
	return &object{Meta: meta}, nil
}

// fetchObject retrieves object obj from the given GCS bucket.
// The returned error will be of type fetchError if the storage responds
// with an error code.
func fetchObject(ctx context.Context, bucket, obj string) (*object, error) {
	u := fmt.Sprintf("%s/%s", gcsBase, path.Join(bucket, obj))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if h, ok := ctx.Value(headerKey).(http.Header); ok {
		addUserHeaders(req, h)
	}
	res, err := httpClient(ctx, scopeStorageRead).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// error status code takes precedence over i/o errors
	b, err := ioutil.ReadAll(res.Body)
	if res.StatusCode > 399 {
		return nil, &fetchError{
			msg:  fmt.Sprintf("%s: %s", res.Status, b),
			code: res.StatusCode,
		}
	}
	if err != nil { // i/o error
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

// getObjectCache returns an object previously cached with the key.
func getObjectCache(ctx context.Context, key string) (*object, error) {
	o := &object{}
	_, err := memcache.Gob.Get(ctx, key, o)
	if err != nil && err != memcache.ErrCacheMiss {
		log.Errorf(ctx, "memcache.Gob.Get(%q): %v", key, err)
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
		log.Errorf(ctx, "memcache.Gob.Set(%q): %v", key, err)
	}
	return err
}

// removeObjectCache removes cached object from memcache.
// It returns nil in case where memcache.Delete would result in ErrCacheMiss.
func removeObjectCache(ctx context.Context, bucket, obj string) error {
	k := path.Join(bucket, obj)
	err := memcache.Delete(ctx, k)
	if err == memcache.ErrCacheMiss {
		err = nil
	}
	return err
}

// useCache reports whether the in-flight request associated with ctx
// can be responded to with a cached version of an object.
//
// It returns false if either "Range", "Origin" or any of conditional headers
// are present in the request.
func useCache(ctx context.Context) bool {
	h, ok := ctx.Value(headerKey).(http.Header)
	if !ok {
		return true
	}
	for k := range h {
		if k == "Range" || k == "Origin" || strings.HasPrefix(k, "If-") {
			return false
		}
	}
	return true
}

// httpClient returns an authenticated http client, suitable for App Engine.
func httpClient(c context.Context, scopes ...string) *http.Client {
	t := &oauth2.Transport{
		Source: google.AppEngineTokenSource(c, scopes...),
		Base:   &urlfetch.Transport{Context: c},
	}
	return &http.Client{Transport: t}
}

// addUserHeaders sets headers on r from h, for all elements of userHeaders.
func addUserHeaders(r *http.Request, h http.Header) {
	for _, k := range userHeaders {
		if v, ok := h[k]; ok {
			r.Header[k] = v
		}
	}
}

type fetchError struct {
	msg  string
	code int
}

func (e *fetchError) Error() string {
	return fmt.Sprintf("fetchError %d: %s", e.code, e.msg)
}
