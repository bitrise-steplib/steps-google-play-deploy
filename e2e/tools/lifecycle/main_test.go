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

func TestInspectAndPrint(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/applications/com.example.app/edits":
			_, _ = w.Write([]byte(`{"id":"edit-1","expiryTimeSeconds":"1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/applications/com.example.app/edits/edit-1/tracks":
			_, _ = w.Write([]byte(`{"tracks":[
				{"track":"internal","releases":[{"name":"Overwritten release name","status":"inProgress","userFraction":0.6,"versionCodes":["359616"]}]},
				{"track":"apk-testing","releases":[{"name":"359617","status":"completed","versionCodes":["359617"]}]},
				{"track":"production"}
			]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/applications/com.example.app/edits/edit-1":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/applications/com.example.app/tracks/internal/releases" && r.URL.Query().Get("pageToken") == "":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"Overwritten release name","track":"internal","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_PUBLISHED","activeArtifacts":[{"versionCode":"359616"}]}],"nextPageToken":"p2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/applications/com.example.app/tracks/internal/releases" && r.URL.Query().Get("pageToken") == "p2":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"older","track":"internal","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_NOT_APPROVED","activeArtifacts":[{"versionCode":359000}]}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/applications/com.example.app/tracks/apk-testing/releases":
			_, _ = w.Write([]byte(`{"releases":[{"releaseName":"359617","track":"apk-testing","releaseLifecycleState":"RELEASE_LIFECYCLE_STATE_IN_REVIEW","activeArtifacts":[{"versionCode":"359617"}]}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/applications/com.example.app/tracks/production/releases":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected call"}`))
		}
	}))
	defer server.Close()

	r, err := inspect(context.Background(), server.Client(), server.URL, "com.example.app", nil)
	require.NoError(t, err)

	require.Len(t, r.Tracks, 3)
	require.Equal(t, "RELEASE_LIFECYCLE_STATE_IN_REVIEW", r.Lifecycle["apk-testing"][0].ReleaseLifecycleState)
	require.Len(t, r.Lifecycle["internal"], 2, "both pages are read")
	require.Equal(t, "359000", r.Lifecycle["internal"][1].ActiveArtifacts[0].code(), "an unquoted version code is tolerated")
	require.Contains(t, r.Errors["production"].Error(), "HTTP 403")
	require.Contains(t, calls, "DELETE /applications/com.example.app/edits/edit-1", "the inspection edit is deleted")

	var out bytes.Buffer
	printReport(&out, "after the step", r)
	text := out.String()
	require.Contains(t, text, "== Play state: after the step (com.example.app) ==")
	require.Contains(t, text, "internal")
	require.Contains(t, text, "inProgress")
	require.Contains(t, text, "0.6")
	require.Regexp(t, `production\s+\(no releases\)`, text)
	require.Contains(t, text, "RELEASE_LIFECYCLE_STATE_IN_REVIEW")
	require.Regexp(t, `internal\s+RELEASE_LIFECYCLE_STATE_NOT_APPROVED\s+359000\s+older`, text)
	require.Contains(t, text, "error: HTTP 403")
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

func TestInspectLimitsLifecycleToWantedTracks(t *testing.T) {
	var mu sync.Mutex
	var lifecycleCalls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"edit-1"}`))
		case r.URL.Path == "/applications/com.example.app/edits/edit-1/tracks":
			_, _ = w.Write([]byte(`{"tracks":[{"track":"internal"},{"track":"apk-testing"},{"track":"production"}]}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{}`))
		default:
			mu.Lock()
			lifecycleCalls = append(lifecycleCalls, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"releases":[]}`))
		}
	}))
	defer server.Close()

	_, err := inspect(context.Background(), server.Client(), server.URL, "com.example.app", splitList(" apk-testing, internal,,"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"/applications/com.example.app/tracks/internal/releases",
		"/applications/com.example.app/tracks/apk-testing/releases",
	}, lifecycleCalls)
}

func TestCallWithQuotaBackoffRetries(t *testing.T) {
	quotaBackoff = 0
	defer func() { quotaBackoff = 20 * time.Second }()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
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
	require.Equal(t, 3, attempts)
	require.Len(t, page.Releases, 1)
}
