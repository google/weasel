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
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"

	"golang.org/x/net/context"
)

const (
	// object custom metadata
	metaRedirect     = "x-goog-meta-redirect"
	metaRedirectCode = "x-goog-meta-redirect-code"

	// memcache settings
	cacheItemMax    = 1 << 20 // max size per item, in bytes
	cacheItemExpiry = 24 * time.Hour
)

// objectHeaders is a slice of headers propagated from a GCS object.
var objectHeaders = []string{
	"cache-control",
	"content-disposition",
	"content-type",
	"etag",
	"last-modified",
	metaRedirect,
	metaRedirectCode,
}

// Object represents a single GCS object.
type Object struct {
	Meta map[string]string
	Body io.ReadCloser
}

// Redirect returns o's redirect URL, zero string otherwise.
func (o *Object) Redirect() string {
	return o.Meta[metaRedirect]
}

// RedirectCode returns o's HTTP response code for redirect.
// It defaults to http.StatusMovedPermanently.
func (o *Object) RedirectCode() int {
	c, err := strconv.Atoi(o.Meta[metaRedirectCode])
	if err != nil {
		c = http.StatusMovedPermanently
	}
	return c
}

// objectBuf implements io.ReadCloser for Object.Body.
// It stores all r.Read results in its buf and caches exported fields
// in memcache when Read returns io.EOF.
type objectBuf struct {
	Meta map[string]string
	Body []byte // set after rc returns io.EOF

	r   io.Reader
	buf bytes.Buffer
	key string          // cache key
	ctx context.Context // memcache context
}

func (b *objectBuf) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 && b.buf.Len() < cacheItemMax {
		b.buf.Write(p[:n])
	}
	if err == io.EOF && b.buf.Len() < cacheItemMax {
		b.Body = b.buf.Bytes()
		item := memcache.Item{
			Key:        b.key,
			Object:     b,
			Expiration: cacheItemExpiry,
		}
		if err := memcache.Gob.Set(b.ctx, &item); err != nil {
			log.Errorf(b.ctx, "memcache.Gob.Set(%q): %v", b.key, err)
		}
	}
	return n, err
}

func (b *objectBuf) Close() error {
	if c, ok := b.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
