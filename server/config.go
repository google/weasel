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
	"encoding/json"
	"os"

	"github.com/google/weasel"
)

// configFile is the frontend server config file.
// See appConfig for fields description.
const configFile = "config.json"

// config is the global app config instance.
var config appConfig

// appConfig is the frontend server config.
type appConfig struct {
	// Redirects is a map of URLs the app will permanently redirect to
	// when the request host and path match a key.
	// Map values must not end with "/" and cannot contain query string.
	Redirects map[string]string `json:"redirects"`

	// CacheSize is the size in bytes for an in-memory cache. A value of 0
	// will result in no cache being created
	CacheSize int `json:"cacheSize"`

	// LocalCacheTTL is the maximum number of seconds an object will stay in local
	// memory. This is necessary because purgeCache will only run
	// on a single instance. If not set, defaults to 10 minutes.
	// If you never want it to timeout, set to -1
	LocalCacheTTL int `json:"localCacheTTL"`

	// TLSOnly will force TLS connection for the specified host names.
	TLSOnly []string `json:"tlsonly"`
	// tlsOnly is an internal map created from TLSOnly config field.
	tlsOnly map[string]struct{}

	// Buckets defines a mapping between hosts
	// and GCS buckets the responses should be served from.
	// The map must contain at least "default" key.
	Buckets map[string]string `json:"buckets"`

	WebRoot  string `json:"webroot"` // default handler pattern
	Index    string `json:"index"`   // dir index file name
	HookPath string `json:"hook"`    // GCS object change notification hook pattern
	GCSBase  string `json:"gcs"`     // GCS base URL
}

// readConfig reads file contents from configFile and populates config.
// JSON is the expected config file format.
func readConfig() error {
	r, err := os.Open(configFile)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := json.NewDecoder(r).Decode(&config); err != nil {
		return err
	}
	if config.WebRoot == "" {
		config.WebRoot = "/"
	}
	if config.HookPath == "" {
		config.HookPath = "/-/hook/gcs"
	}
	if config.GCSBase == "" {
		config.GCSBase = weasel.DefaultStorage.Base
	}
	if config.LocalCacheTTL == 0 {
		config.LocalCacheTTL = 600
	}
	config.tlsOnly = make(map[string]struct{})
	for _, v := range config.TLSOnly {
		config.tlsOnly[v] = struct{}{}
	}
	return nil
}
