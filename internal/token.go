// Package internal contains utilities internal to the weasel packages.
package internal

import (
	"context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// AETokenSource returns App Engine OAuth2 token source
// given a context.Context and a slice of scopes.
// It is a stubbed static token source during testing.
var AETokenSource = func(ctx context.Context, scope ...string) oauth2.TokenSource {
  ts, err := google.DefaultTokenSource(ctx, scope...)
  if (err != nil) {
    panic(err)
  }
  return ts
}
