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
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
)

var (
	// allowMethods is a slice of allow methods when requesting a GCS object.
	// The slice must be sorted in lexical order.
	allowMethods = []string{
		"GET",
		"HEAD",
		"OPTIONS",
	}
	// allowMethodsStr is allowMethods in a single string version,
	// suitable for Allow or CORS allow methods header.
	allowMethodsStr = strings.Join(allowMethods, ", ")
)

// ServeObject writes object o to w, with optional body.
func ServeObject(w http.ResponseWriter, o *Object, withBody bool) error {
	if v := o.Redirect(); v != "" {
		w.Header().Set("location", v)
		w.WriteHeader(o.RedirectCode())
		return nil
	}

	// headers
	h := w.Header()
	for k, v := range o.Meta {
		h.Set(k, v)
	}
	h.Set("allow", allowMethodsStr)
	// body
	var err error
	if withBody {
		_, err = w.Write(o.Body)
	}
	return err
}

// HandleChangeHook handles Object Change Notifications as described at
// https://cloud.google.com/storage/docs/object-change-notification.
// It removes objects from cache.
func (s *Storage) HandleChangeHook(w http.ResponseWriter, r *http.Request) {
	// skip sync requests
	if v := r.Header.Get("x-goog-resource-state"); v == "sync" {
		return
	}

	// this is not a client request, so don't use newContext.
	ctx := appengine.NewContext(r)
	// we only care about name and the bucket
	body := struct{ Name, Bucket string }{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Errorf(ctx, "json.Decode: %v", err.Error())
		return
	}
	if err := s.PurgeCache(ctx, body.Bucket, body.Name); err != nil {
		log.Errorf(ctx, "s.PurgeCache(%q, %q): %v", body.Bucket, body.Name, err)
		w.WriteHeader(http.StatusInternalServerError) // let GCS retry
	}
}

// ValidMethod reports whether m is a supported HTTP method.
func ValidMethod(m string) bool {
	i := sort.SearchStrings(allowMethods, m)
	return i < len(allowMethods) && allowMethods[i] == m
}
