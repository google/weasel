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

// Package server provides a simple frontend in form of an App Engine app
// built atop the weasel.Storage.
// See README.md for the design details.
//
// This package is a work in progress and makes no API stability promises.
//
// An example usage for App Engine Standard:
//
//    # app.yaml
//    runtime: go
//    api_version: go1
//    handlers:
//    - url: /.*
//      script: _go_app
//
//    # app.go
//    package app
//
//    import (
//      "github.com/google/weasel"
//      "github.com/google/weasel/server"
//    )
//
//    func init() {
//      conf := &server.Config{
//        Storage: weasel.DefaultStorage,
//        Buckets: map[string]string{
//          "default": "my-gcs-bucket",
//        },
//        HookPath: "/-/flush-gcs-cache",
//      }
//      server.Init(nil, conf)
//    }
//
// The "/-/flush-gcs-cache" needs to be hooked up with "my-gcs-bucket" manually
// using Object Change Notifications. See the following page for more details:
// https://cloud.google.com/storage/docs/object-change-notification
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/google/weasel"
	"google.golang.org/appengine/log"

	"google.golang.org/appengine"
)

// Used to set STS header value when serving over TLS.
const stsValue = "max-age=10886400; includeSubDomains; preload"

// KeyPathFn is a function that determines the lookup key that maps to
// the appropriate bucket and the adjusted file path from a HTTP request
type KeyPathFn func(r *http.Request) (bucketKey, filePath string)

// Init registers server handlers on the provided mux.
// If the mux argument is nil, http.DefaultServeMux is used.
//
// See package doc for a usage example.
func Init(mux *http.ServeMux, conf *Config) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	for host, redir := range conf.Redirects {
		mux.Handle(host, redirectHandler(redir, http.StatusMovedPermanently))
	}
	s := &server{
		storage: conf.Storage,
		buckets: conf.Buckets,
		keyPathFn: conf.KeyPathFn,
		tlsOnly: make(map[string]struct{}, len(conf.TLSOnly)),
	}
	for _, h := range conf.TLSOnly {
		s.tlsOnly[h] = struct{}{}
	}

	// if not set, set the KeyPathFn to a default function that returns
	// the `host` header as the key and the relative path
	if s.keyPathFn == nil {
		s.keyPathFn = func(r *http.Request) (string, string) {
			return r.Host, r.URL.Path[1:]
		}
	}

	mux.Handle(conf.webroot(), s)
	if conf.HookPath != "" {
		mux.HandleFunc(conf.HookPath, conf.Storage.HandleChangeHook)
	}
}

// Config is used to init the server.
// See Init for more details.
type Config struct {
	// Storage provides server with the content access.
	Storage *weasel.Storage

	// Buckets defines a mapping between a request attribute
	// and GCS buckets the responses should be served from.
	// The map must contain at least "default" key.
	Buckets map[string]string

	// KeyPathFn is a function that returns the appropriate key
	// that maps to a bucket and file path for a HTTP request.
	// Default is a function that returns the `host` HTTP header as
	// a bucket key and the relative path.
	KeyPathFn KeyPathFn

	// WebRoot is the content serving root pattern.
	// If empty, default is used.
	// Default value is "/".
	WebRoot string

	// GCS object change notification hook pattern.
	// If empty, no hook handler will be setup during Init.
	HookPath string

	// Redirects is a map of URLs the app will permanently redirect to
	// when the request host and path match a key.
	// Map values must not end with "/" and cannot contain query string.
	Redirects map[string]string

	// TLSOnly forces TLS connection for the specified host names.
	TLSOnly []string
}

func (c *Config) webroot() string {
	if c.WebRoot != "" {
		return c.WebRoot
	}
	return "/"
}

type server struct {
	// The GCS storage to serve content from.
	storage *weasel.Storage

	// Contains hostnames forced to be server over TLS.
	tlsOnly map[string]struct{}

	// Defines a mapping between keys
	// and GCS buckets the responses should be served from.
	// The map must contain at least "default" key.
	buckets map[string]string

	// keyPathFn is a function that returns the appropriate key
	// that maps to a bucket and file path for a HTTP request.
	keyPathFn KeyPathFn
}

// ServeHTTP responds with a GCS object contents, preserving its original headers
// listed in objectHeaders.
// The backend bucket is determined by doing a lookup of the key returned by
// s.keyPathFn(). Default bucket is used if no match is found.
//
// Only GET, HEAD and OPTIONS methods are allowed.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, forceTLS := s.tlsOnly[r.Host]
	if forceTLS && r.Header.Get("X-Forwarded-Proto") == "https" {
		w.Header().Set("Strict-Transport-Security", stsValue)
	}
	if !weasel.ValidMethod(r.Method) {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	if forceTLS && r.Header.Get("X-Forwarded-Proto") == "http" {
		u := "https://" + r.Host + r.URL.Path
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusMovedPermanently)
		return
	}

	ctx, cancel := context.WithTimeout(appengine.NewContext(r), 10*time.Second)
	defer cancel()

	bucketKey, oname := s.keyPathFn(r)
	bucket := s.bucketLookup(bucketKey)

	o, err := s.storage.OpenFile(ctx, bucket, oname)
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
	if err := s.storage.ServeObject(w, r, o); err != nil {
		log.Errorf(ctx, "%s/%s: %v", bucket, oname, err)
	}
	o.Body.Close()
}

// bucketLookup returns a bucket name mapped to the provided key.
// Default bucket name is returned if no match is found.
func (s *server) bucketLookup(key string) string {
	if b, ok := s.buckets[key]; ok {
		return b
	}
	return s.buckets["default"]
}

// redirectHandler creates a new handler which redirects all requests
// to the specified url, preserving original path and raw query.
func redirectHandler(url string, code int) http.Handler {
	// TODO: parse url and support path, query, etc.
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
