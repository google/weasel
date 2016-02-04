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
	"strconv"
)

const (
	// object custom metadata
	metaRedirect     = "x-goog-meta-redirect"
	metaRedirectCode = "x-goog-meta-redirect-code"
)

// objectHeaders is a slice of headers propagated from a GCS object.
var objectHeaders = []string{
	"cache-control",
	"content-disposition",
	"content-type",
	"etag",
	"last-modified",
}

// Object represents a single GCS object.
type Object struct {
	Meta map[string]string
	Body []byte
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
