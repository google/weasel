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
	"strings"
	"time"

	"github.com/google/weasel/internal"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"

	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/urlfetch"
)

// Google Cloud Storage OAuth2 scopes
const (
	ScopeStorageRead  = "https://www.googleapis.com/auth/devstorage.read_only"
	ScopeStorageOwner = "https://www.googleapis.com/auth/devstorage.full_control"
)

// DefaultStorage is a Storage with sensible default parameters
// suitable for prod environments on App Engine.
var DefaultStorage = Storage{
	Base:  "https://storage.googleapis.com",
	Index: "index.html",
}

// Storage incapsulates configuration params for retrieveing and serving GCS objects.
type Storage struct {
	Base  string // GCS service base URL, e.g. "https://storage.googleapis.com".
	Index string // Appended to an object name in certain cases, e.g. "index.html".
}

// ReadFile abstracts ReadObject and treats object name like a file path.
func (s *Storage) ReadFile(ctx context.Context, bucket, name string) (*Object, error) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += s.Index
	}

	// stat /dir/index.html if name is /dir, concurrently
	var (
		statc   = make(chan struct{})
		statdir *Object
		staterr error
	)
	if !strings.HasSuffix(name, s.Index) && filepath.Ext(name) == "" {
		go func() {
			idx := path.Join(name, s.Index)
			statdir, staterr = s.Stat(ctx, bucket, idx)
			close(statc)
		}()
	} else {
		close(statc)
	}

	// get the original object meanwhile
	o, err := s.ReadObject(ctx, bucket, name)
	if err == nil {
		return o, nil
	}
	// return non-404 errors right away
	if ferr, ok := err.(*FetchError); ok && ferr.Code != http.StatusNotFound {
		return nil, err
	}

	// wait some time for stat obj
	select {
	case <-time.After(5 * time.Second):
		log.Errorf(ctx, "s.Stat(bucket=%q) timeout", bucket)
	case <-statc:
		// done stat-ing /dir/index
	}
	if staterr != nil || statdir == nil {
		// it's not a "directory" either; return the original error
		return nil, err
	}
	if statdir.Redirect() == "" {
		statdir = &Object{Meta: map[string]string{
			metaRedirect: path.Join("/", name) + "/",
		}}
	}
	return statdir, nil
}

// ReadObject retrieves GCS object name of the bucket from cache or network.
// Objects fetched from the network are cached before returning
// from this function.
func (s *Storage) ReadObject(ctx context.Context, bucket, name string) (*Object, error) {
	key := s.CacheKey(bucket, name)
	o, err := getCache(ctx, key)
	if err != nil {
		o, err = s.fetch(ctx, bucket, name)
		if err == nil {
			putCache(ctx, key, o)
		}
	}
	return o, err
}

// Stat is similar to Read except the returned object.Body may be nil.
func (s *Storage) Stat(ctx context.Context, bucket, name string) (*Object, error) {
	if o, err := getCache(ctx, s.CacheKey(bucket, name)); err == nil {
		return o, nil
	}
	u := fmt.Sprintf("%s/%s", s.Base, path.Join(bucket, name))
	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient(ctx, ScopeStorageRead).Do(req)
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

// fetch retrieves object obj from the given GCS bucket.
// The returned error will be of type FetchError if the storage responds
// with an error code.
func (s *Storage) fetch(ctx context.Context, bucket, obj string) (*Object, error) {
	u := fmt.Sprintf("%s/%s", s.Base, path.Join(bucket, obj))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient(ctx, ScopeStorageRead).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// error status code takes precedence over i/o errors
	b, err := ioutil.ReadAll(res.Body)
	if res.StatusCode > 399 {
		return nil, &FetchError{
			Msg:  fmt.Sprintf("%s: %s", res.Status, b),
			Code: res.StatusCode,
		}
	}
	if err != nil { // i/o error
		return nil, err
	}

	o := &Object{
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

func getCache(ctx context.Context, key string) (*Object, error) {
	o := &Object{}
	_, err := memcache.Gob.Get(ctx, key, o)
	if err != nil && err != memcache.ErrCacheMiss {
		log.Errorf(ctx, "memcache.Gob.Get(%q): %v", key, err)
	}
	return o, err
}

func putCache(ctx context.Context, key string, o *Object) error {
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
