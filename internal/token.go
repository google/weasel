package internal

import "golang.org/x/oauth2/google"

// AETokenSource returns App Engine OAuth2 token source
// given a context.Context and a slice of scopes.
// It is a stubbed static token source during testing.
var AETokenSource = google.AppEngineTokenSource
