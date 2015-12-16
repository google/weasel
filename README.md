# yummy-weasel

A simple frontend (App Engine app) that serves content from a Google
Cloud Storage (GCS) bucket, while allowing for:

- HTTPS on custom domain
- support for Service Worker (works only over HTTPS)
- HTTP/2 push
- more robust redirect from naked custom domain
- serving directly from naked custom domain
- keep the deployment/publishing flow using just the existing GCS bucket,
  which has proven to be fast and reliable, with partial content updates.
- dynamic server-side logic

## design

The design is simple. Suppose you have a static website www.example.com,
served directly from a GCS gs://www.example.com.

A very simplified picture of the serving path can be shown as follows.

    +-----+    GET http://www.example.com/dir/index.html
    | GCS |  <-----------------------------------------+  visitor
    +-----+                                                 :-/

Weasel enhancement consists of modifying the serving path to:

    +-----+   (2) GET gs://www.example.com/dir/index.html
    | GCS |  <------------------------------+--------+
    +-----+                                 | yummy  |
                                            | weasel |
                                            +--------+
                                               ↑  |
                                          (1)  |  | (3)
             GET https://www.example.com/dir/  |  ↓ Link: <asset>; rel=preload

                                             visitor
                                               :-)

1. A visitor requests the website page. Note that /index.html is now optional.
   Due to GCS restrictions, such suffixes were previously required for
   sub-folders, but with this approach they no longer need to be specified.
   Also, requests can (and will) be made over HTTPS.

2. Weasel fetches the object content from the original GCS bucket and caches
   it locally. This step is necessary only if the object hasn't been cached
   already or the cache has expired. Cache expiration and invalidation is
   based on GCS object cache-control header settings.

3. Weasel responds with the GCS object contents. Note that we can optionally
   [push](https://w3c.github.io/preload/) additional assets related to the
   requested file by using `Link: <asset>; rel=preload` header supported
   by GFE.

## dev flow

Just use `goapp` tool provided with the
[Google Appengine SDK for Go](https://cloud.google.com/appengine/downloads).

Assuming the SDK is installed in `$SDK_DIR`:

- `goapp test` runs tests
- `goapp deploy` deploys the app to App Engine production servers.
