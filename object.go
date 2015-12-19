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
		"content-type",
		"cache-control",
		"content-disposition",
		"last-modified",
		"etag",
		"access-control-allow-origin",
		"access-control-allow-methods",
		"access-control-allow-headers",
		"access-control-allow-credentials",
		"access-control-max-age",
		"access-control-expose-headers",
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
	if err != nil {
		// check for /dir/index.html
		<-donec
		if odirErr != nil || odir == nil {
			// it's not a "directory" either; return the original error
			return nil, err
		}
		o = &object{Meta: map[string]string{
			metaRedirect: idxname[:len(idxname)-len(defaultIndex)],
		}}
		err = nil
	}

	return o, err
}

// getObject retrieves GCS object obj of the bucket from cache or network.
// Objects fetched from the network are cached before returning
// from this function.
func getObject(ctx context.Context, bucket, obj string) (*object, error) {
	key := objectCacheKey(bucket, obj)
	o := &object{}
	_, err := memcache.Gob.Get(ctx, key, o)
	if err == nil {
		return o, nil
	}
	if err != memcache.ErrCacheMiss {
		log.Errorf(ctx, "memcache.Get(%q): %v", key, err)
	}

	o, err = fetchObject(ctx, bucket, obj)
	if err != nil {
		return nil, err
	}
	item := memcache.Item{
		Key:        key,
		Object:     o,
		Expiration: 24 * time.Hour,
	}
	if err := memcache.Gob.Set(ctx, &item); err != nil {
		log.Errorf(ctx, "memcache.Set(%q): %v", key, err)
	}
	return o, nil
}

// fetchObject retrieves object obj from the given GCS bucket.
// The returned error will be of type fetchError if the storage responds
// with an error code.
func fetchObject(ctx context.Context, bucket, obj string) (*object, error) {
	u := fmt.Sprintf("%s/%s/%s", gcsBase, bucket, obj)
	res, err := httpClient(ctx, scopeStorageOwner).Get(u)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := ioutil.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
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

// removeObjectCache removes cached object from memcache.
func removeObjectCache(ctx context.Context, bucket, obj string) error {
	key := objectCacheKey(bucket, obj)
	return memcache.Delete(ctx, key)
}

// objectCacheKey returns a cache key for object obj and the given bucket.
func objectCacheKey(bucket, obj string) string {
	return path.Join(bucket, obj)
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
