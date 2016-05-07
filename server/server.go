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

package server

import (
	"net/http"
	"time"

	"golang.org/x/net/context"

	"github.com/google/weasel"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
)

// storage is used by the weasel server to serve GCS objects.
var storage *weasel.Storage

func init() {
	if err := readConfig(); err != nil {
		panic(err)
	}
	storage = &weasel.Storage{Base: config.GCSBase, Index: config.Index}
	for host, redir := range config.Redirects {
		http.Handle(host, redirectHandler(redir, http.StatusMovedPermanently))
	}
	http.HandleFunc(config.WebRoot, serveObject)
	http.HandleFunc(config.HookPath, storage.HandleChangeHook)
}

// serveObject responds with a GCS object contents, preserving its original headers
// listed in objectHeaders.
// The bucket is identifed by matching r.Host against config.Buckets map keys.
// Default bucket is used if no match is found.
//
// Only GET, HEAD and OPTIONS methods are allowed.
func serveObject(w http.ResponseWriter, r *http.Request) {
	if !weasel.ValidMethod(r.Method) {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	if _, force := config.tlsOnly[r.Host]; force && r.TLS == nil {
		u := "https://" + r.Host + r.URL.Path
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusMovedPermanently)
		return
	}

	ctx := newContext(r)
	bucket := bucketForHost(r.Host)
	oname := r.URL.Path[1:]

	o, err := storage.ReadFile(ctx, bucket, oname)
	if err != nil {
		code := http.StatusInternalServerError
		if errf, ok := err.(*weasel.FetchError); ok {
			code = errf.Code
		}
		serveError(w, code, "")
		if code != http.StatusNotFound {
			log.Errorf(ctx, "%s/%s: %v", bucket, oname, err)
		}
		return
	}

	if err := storage.ServeObject(w, r, o); err != nil {
		log.Errorf(ctx, "%s/%s: %v", bucket, oname, err)
	}
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

func serveError(w http.ResponseWriter, code int, msg string) {
	if msg == "" {
		msg = http.StatusText(code)
	}
	w.WriteHeader(code)
	// TODO: render some template
	w.Write([]byte(msg))
}

// bucketForHost returns a bucket name mapped to the host.
// Default bucket name is return if no match found.
func bucketForHost(host string) string {
	if b, ok := config.Buckets[host]; ok {
		return b
	}
	return config.Buckets["default"]
}

// newContext creates a new context from a client in-flight request.
// It should not be used for server-to-server, such as web hooks.
func newContext(r *http.Request) context.Context {
	c := appengine.NewContext(r)
	c, _ = context.WithTimeout(c, 10*time.Second)
	return c
}
