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
	"testing"
)

func TestObjectRedirect(t *testing.T) {
	o := &Object{Meta: map[string]string{metaRedirect: "/new/path"}}
	if v := o.Redirect(); v != "/new/path" {
		t.Errorf("o.Redirect() = %q; want /new/path", v)
	}
	if v := o.RedirectCode(); v != http.StatusMovedPermanently {
		t.Errorf("o.RedirectCode() = %d; want %d", v, http.StatusMovedPermanently)
	}

	o = &Object{Meta: map[string]string{metaRedirectCode: "333"}}
	if v := o.RedirectCode(); v != 333 {
		t.Errorf("o.RedirectCode() = %d; want 333", v)
	}

	o = &Object{Meta: map[string]string{metaRedirectCode: "invalid"}}
	if v := o.RedirectCode(); v != http.StatusMovedPermanently {
		t.Errorf("o.RedirectCode() = %d; want %d", v, http.StatusMovedPermanently)
	}
}
