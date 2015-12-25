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
	"time"

	"golang.org/x/net/context"

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

func init() {
	if err := readConfig(&config, configFile); err != nil {
		panic(err)
	}
	for host, redir := range config.Redirects {
		http.Handle(host, redirectHandler(redir, http.StatusMovedPermanently))
	}
	http.HandleFunc("/", serveObject)
	http.HandleFunc("/-/hook/gcs", handleHook)
}

// serveObject responds with a GCS object contents, preserving its original headers
// listed in objectHeaders.
// The bucket is identifed by matching r.Host against config.Buckets map keys.
// Default bucket is used if no match is found.
//
// Only GET, HEAD and OPTIONS methods are allowed.
func serveObject(w http.ResponseWriter, r *http.Request) {
	if !validMethod(r.Method) {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	ctx := newContext(r)
	bucket := bucketForHost(r.Host)
	oname := r.URL.Path[1:]

	o, err := getFile(ctx, bucket, oname)
	if err != nil {
		code := http.StatusInternalServerError
		if errf, ok := err.(*fetchError); ok {
			code = errf.code
		}
		serveError(w, code, "")
		if code != http.StatusNotFound {
			log.Errorf(ctx, "%s/%s: %v", bucket, oname, err)
		}
		return
	}

	if v := o.redirect(); v != "" {
		http.Redirect(w, r, v, o.redirectCode())
		return
	}

	// headers
	h := w.Header()
	for k, v := range o.Meta {
		h.Set(k, v)
	}
	h.Set("allow", allowMethodsStr)
	if _, ok := h["Access-Control-Allow-Origin"]; ok {
		h.Set("access-control-allow-methods", allowMethodsStr)
	}
	// body, but only if GET request
	if r.Method == "GET" {
		_, err := w.Write(o.Body)
		if err != nil {
			log.Errorf(ctx, "%s/%s: %v", bucket, oname, err)
		}
	}
}

// handleHook handles Object Change Notifications as described at
// https://cloud.google.com/storage/docs/object-change-notification.
// It removes objects from cache.
func handleHook(w http.ResponseWriter, r *http.Request) {
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
	if err := removeObjectCache(ctx, body.Bucket, body.Name); err != nil {
		log.Errorf(ctx, "removeObjectCache: %v", err)
		// let GCS retry
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func serveError(w http.ResponseWriter, code int, msg string) {
	if msg == "" {
		msg = http.StatusText(code)
	}
	w.WriteHeader(code)
	// TODO: render some template
	w.Write([]byte(msg))
}

// redirectHandler creates a new handler which redirects all requests
// to the specified url, preserving original path and raw query.
func redirectHandler(url string, code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := url + r.URL.Path
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, code)
	})
}

// validMethod reports whether allowMethods contains m.
func validMethod(m string) bool {
	i := sort.SearchStrings(allowMethods, m)
	return i < len(allowMethods) && allowMethods[i] == m
}

// bucketForHost returns a bucket name mapped to the host.
// Default bucket name is return if no match found.
func bucketForHost(host string) string {
	if b, ok := config.Buckets[host]; ok {
		return b
	}
	return config.Buckets["default"]
}

// ctxKey is a context value key
type ctxKey int

const (
	_ ctxKey = iota // ignore 0

	headerKey // in-flight request headers
	methodKey // HTTP verb, e.g. "GET"
)

// newContext creates a new context from a client in-flight request.
// It should not be used for server-to-server, such as web hooks.
func newContext(r *http.Request) context.Context {
	c := appengine.NewContext(r)
	c = context.WithValue(c, headerKey, r.Header)
	c = context.WithValue(c, methodKey, r.Method)
	c, _ = context.WithTimeout(c, 10*time.Second)
	return c
}
