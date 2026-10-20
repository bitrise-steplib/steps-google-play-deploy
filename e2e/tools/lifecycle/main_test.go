package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const pkgPath = "/applications/com.example.app"

func newFakePlay(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls = append(*calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == pkgPath+"/edits":
			_, _ = w.Write([]byte(`{"id":"edit-1","expiryTimeSeconds":"1"}`))
		case r.Method == http.MethodDelete && r.URL.Path == pkgPath+"/edits/edit-1":
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == pkgPath+"/edits/edit-1/tracks":
			_, _ = w.Write([]byte(`{"tracks":[
				{"track":"internal","releases":[{"name":"Overwritten release name","status":"inProgress","userFraction":0.6,"versionCodes":["359616"]}]},
				{"track":"apk-testing","releases":[{"name":"359617","status":"completed","versionCodes":["359617"]}]},
				{"track":"production"}
			]}`))
		case r.URL.Path == pkgPath+"/edits/edit-1/bundles":
			_, _ = w.Write([]byte(`{"kind":"androidpublisher#bundlesListResponse","bundles":[{"versionCode":359616,"sha1":"a"},{"versionCode":359600,"sha1":"b"}]}`))
		case r.URL.Path == pkgPath+"/edits/edit-1/apks":
			_, _ = w.Write([]byte(`{"kind":"androidpublisher#apksListResponse","apks":[{"versionCode":359617},{"versionCode":"359618"}]}`))
		case r.URL.Path == pkgPath+"/tracks/internal/releases" && r.URL.Query().Get("pageToken") == "":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"Overwritten release name","track":"internal","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_PUBLISHED","activeArtifacts":[{"versionCode":"359616"}]}],"nextPageToken":"p2"}`))
		case r.URL.Path == pkgPath+"/tracks/internal/releases" && r.URL.Query().Get("pageToken") == "p2":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"older","track":"internal","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_NOT_APPROVED","activeArtifacts":[{"versionCode":359000}]}]}`))
		case r.URL.Path == pkgPath+"/tracks/apk-testing/releases":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"359617","track":"apk-testing","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_IN_REVIEW","activeArtifacts":[{"versionCode":"359617"}]}]}`))
		case r.URL.Path == pkgPath+"/tracks/production/releases":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected call"}`))
		}
	}))
}

func TestInspectWithoutLifecycle(t *testing.T) {
	var calls []string
	server := newFakePlay(t, &calls)
	defer server.Close()

	r, err := inspect(context.Background(), server.Client(), server.URL, "com.example.app", inspectOptions{})
	require.NoError(t, err)

	require.Len(t, r.Tracks, 3)
	require.Equal(t, []string{"359616", "359600"}, r.Bundles)
	require.Equal(t, []string{"359617", "359618"}, r.Apks, "quoted and unquoted version codes are both read")
	require.False(t, r.LifecycleRead)
	require.Contains(t, calls, "DELETE "+pkgPath+"/edits/edit-1", "the inspection edit is deleted")
	for _, c := range calls {
		require.NotContains(t, c, "/tracks/internal/releases", "the lifecycle endpoint is not called unless asked")
	}

	var out bytes.Buffer
	printReport(&out, "after the step", r)
	text := out.String()
	require.Contains(t, text, "== Play state: after the step (com.example.app) ==")
	require.Regexp(t, `internal\s+inProgress\s+359616\s+0.6\s+Overwritten release name`, text)
	require.Regexp(t, `production\s+\(no releases\)`, text)
	require.Contains(t, text, "bundles: 359616 (internal), 359600 (not on any track)")
	require.Contains(t, text, "apks:    359617 (apk-testing), 359618 (not on any track)")
	require.Contains(t, text, "review lifecycle (applications.tracks.releases): not read")
}

func TestInspectWithLifecycle(t *testing.T) {
	var calls []string
	server := newFakePlay(t, &calls)
	defer server.Close()

	r, err := inspect(context.Background(), server.Client(), server.URL, "com.example.app", inspectOptions{Lifecycle: true})
	require.NoError(t, err)

	require.True(t, r.LifecycleRead)
	require.Equal(t, "RELEASE_LIFECYCLE_STATE_IN_REVIEW", r.Lifecycle["apk-testing"][0].ReleaseLifecycleState)
	require.Len(t, r.Lifecycle["internal"], 2, "both pages are read")
	require.Contains(t, r.Errors["production"].Error(), "HTTP 403")

	var out bytes.Buffer
	printReport(&out, "", r)
	text := out.String()
	require.Regexp(t, `apk-testing\s+RELEASE_LIFECYCLE_STATE_IN_REVIEW\s+359617\s+359617`, text)
	require.Regexp(t, `internal\s+RELEASE_LIFECYCLE_STATE_NOT_APPROVED\s+359000\s+older`, text)
	require.Contains(t, text, "error: HTTP 403")
}

func TestInspectLimitsLifecycleToWantedTracks(t *testing.T) {
	var calls []string
	server := newFakePlay(t, &calls)
	defer server.Close()

	r, err := inspect(context.Background(), server.Client(), server.URL, "com.example.app", inspectOptions{Lifecycle: true, Tracks: splitList(" apk-testing,,")})
	require.NoError(t, err)

	var lifecycleCalls []string
	for _, c := range calls {
		if bytes.Contains([]byte(c), []byte("/releases")) {
			lifecycleCalls = append(lifecycleCalls, c)
		}
	}
	require.Equal(t, []string{"GET " + pkgPath + "/tracks/apk-testing/releases"}, lifecycleCalls)

	var out bytes.Buffer
	printReport(&out, "", r)
	require.Regexp(t, `internal\s+\(not read\)`, out.String())
}

func TestCallWithQuotaBackoffRetries(t *testing.T) {
	quotaBackoff = 0
	defer func() { quotaBackoff = 20 * time.Second }()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < quotaAttempts {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Listing releases quota exceeded.","status":"PERMISSION_DENIED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"releases":[{"releaseName":"r","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_PUBLISHED"}]}`))
	}))
	defer server.Close()

	var page struct {
		Releases []releaseSummary `json:"releases"`
	}
	err := callWithQuotaBackoff(context.Background(), server.Client(), server.URL+"/releases", &page)
	require.NoError(t, err)
	require.Equal(t, quotaAttempts, attempts)
	require.Len(t, page.Releases, 1)
}

func TestCallReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Changes are sent for review automatically. The query parameter changesNotSentForReview must not be set."}}`))
	}))
	defer server.Close()

	err := call(context.Background(), server.Client(), http.MethodGet, server.URL+"/x", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 400")
	require.Contains(t, err.Error(), "must not be set")
}
