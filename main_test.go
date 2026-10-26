package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bitrise-io/go-utils/v2/errorutil"
	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestParseURI(t *testing.T) {

	t.Log("parseURI - file://../../../../../../Downloads/key.json")
	{
		keyPth, isRemote, err := parseURI("file://../../../../../../Downloads/key.json")
		require.NoError(t, err)

		require.Equal(t, "../../../../../../Downloads/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - file://./")
	{
		keyPth, isRemote, err := parseURI("file://./testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "./testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - file:///")
	{
		keyPth, isRemote, err := parseURI("file:///testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "/testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - http://")
	{
		keyPth, isRemote, err := parseURI("http://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "http://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("parseURI - https://")
	{
		keyPth, isRemote, err := parseURI("https://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "https://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("parseURI - ./")
	{
		keyPth, isRemote, err := parseURI("./user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "./user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - /")
	{
		keyPth, isRemote, err := parseURI("/user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "/user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}
}

func TestCreateHTTPClientReportsAnInvalidKey(t *testing.T) {
	publisher := NewPublisher(log.NewLogger())

	t.Log("local key file")
	{
		keyPath := filepath.Join(t.TempDir(), "key.json")
		require.NoError(t, os.WriteFile(keyPath, []byte("not a service account key"), 0o600))

		client, err := publisher.createHTTPClient(keyPath)

		require.Error(t, err)
		require.Nil(t, client)
		require.Contains(t, err.Error(), keyPath)
	}

	t.Log("downloaded key file")
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not a service account key"))
		}))
		defer server.Close()

		client, err := publisher.createHTTPClient(server.URL + "/key.json")

		require.Error(t, err, "a key that downloads but does not parse is reported, not swallowed")
		require.Nil(t, client)
		require.Contains(t, err.Error(), "downloaded json key file")
	}
}

func TestListTracksSurvivesAFailedListCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`))
	}))
	defer server.Close()

	service, err := androidpublisher.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	require.NoError(t, err)

	publisher := NewPublisher(log.NewLogger())
	require.NotPanics(t, func() {
		publisher.listTracks(Configs{PackageName: "com.example.app"}, service, &androidpublisher.AppEdit{Id: "edit-1"})
	})
}

// fakePlay serves the subset of the Android Publisher API the deploy flow touches and records what it was sent.
type fakePlay struct {
	server               *httptest.Server
	mu                   sync.Mutex
	commitResponses      []fakeResponse // consumed in order, then commits succeed
	trackUpdateResponses []fakeResponse // consumed in order, then track updates succeed
	validateResponses    []fakeResponse // consumed in order, then validations succeed
	uploadResponses      []fakeResponse // consumed in order, then uploads succeed
	tracksList           *fakeResponse  // when set, the tracks list answers with it
	inserts              int
	uploads              int
	validates            int
	commits              []url.Values
	trackUpdates         []androidpublisher.Track
}

type fakeResponse struct {
	status int
	body   string
}

const fakeEditsPath = "/androidpublisher/v3/applications/com.example.app/edits"

const (
	editBody                  = `{"id":"edit-1"}`
	cannotBeSentForReviewBody = `{"error":{"code":400,"message":"Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true.","status":"INVALID_ARGUMENT"}}`
)

var (
	commitOK           = fakeResponse{status: http.StatusOK, body: editBody}
	commitManualReview = fakeResponse{status: http.StatusBadRequest, body: cannotBeSentForReviewBody}
	serverError        = fakeResponse{status: http.StatusInternalServerError, body: `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`}
)

func newFakePlay(t *testing.T, commitResponses ...fakeResponse) *fakePlay {
	t.Helper()
	f := &fakePlay{commitResponses: commitResponses}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePlay) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == fakeEditsPath:
		f.inserts++
		_, _ = w.Write([]byte(editBody))
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload"+fakeEditsPath+"/edit-1/"):
		f.uploads++
		if i := f.uploads - 1; i < len(f.uploadResponses) {
			w.WriteHeader(f.uploadResponses[i].status)
			_, _ = w.Write([]byte(f.uploadResponses[i].body))
			return
		}
		_, _ = w.Write([]byte(`{"versionCode":42}`))
	case r.Method == http.MethodGet && r.URL.Path == fakeEditsPath+"/edit-1/tracks":
		if f.tracksList != nil {
			w.WriteHeader(f.tracksList.status)
			_, _ = w.Write([]byte(f.tracksList.body))
			return
		}
		_, _ = w.Write([]byte(`{"tracks":[{"track":"internal"},{"track":"production"}]}`))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, fakeEditsPath+"/edit-1/tracks/"):
		body, _ := io.ReadAll(r.Body)
		var track androidpublisher.Track
		if err := json.Unmarshal(body, &track); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if i := len(f.trackUpdates); i < len(f.trackUpdateResponses) {
			f.trackUpdates = append(f.trackUpdates, track)
			w.WriteHeader(f.trackUpdateResponses[i].status)
			_, _ = w.Write([]byte(f.trackUpdateResponses[i].body))
			return
		}
		f.trackUpdates = append(f.trackUpdates, track)
		_, _ = w.Write(body)
	case r.Method == http.MethodPost && r.URL.Path == fakeEditsPath+"/edit-1:validate":
		f.validates++
		if i := f.validates - 1; i < len(f.validateResponses) {
			w.WriteHeader(f.validateResponses[i].status)
			_, _ = w.Write([]byte(f.validateResponses[i].body))
			return
		}
		_, _ = w.Write([]byte(editBody))
	case r.Method == http.MethodPost && r.URL.Path == fakeEditsPath+"/edit-1:commit":
		resp := commitOK
		if i := len(f.commits); i < len(f.commitResponses) {
			resp = f.commitResponses[i]
		}
		f.commits = append(f.commits, r.URL.Query())
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"unexpected call ` + r.Method + " " + r.URL.Path + `"}}`))
	}
}

func (f *fakePlay) service(t *testing.T) *androidpublisher.Service {
	t.Helper()
	service, err := androidpublisher.NewService(context.Background(), option.WithEndpoint(f.server.URL+"/"), option.WithoutAuthentication())
	require.NoError(t, err)
	return service
}

func testConfigs(t *testing.T) Configs {
	t.Helper()
	apk := filepath.Join(t.TempDir(), "app.apk")
	require.NoError(t, os.WriteFile(apk, []byte("not really an apk"), 0o600))
	return Configs{
		PackageName: "com.example.app",
		AppPath:     apk,
		Track:       "internal",
		Logger:      log.NewLogger(),
	}
}

func TestDeployCommitsOnce(t *testing.T) {
	play := newFakePlay(t)
	configs := testConfigs(t)

	require.NoError(t, deployError(t, play, configs))

	require.Equal(t, 1, play.inserts)
	require.Equal(t, 1, play.uploads)
	require.Len(t, play.trackUpdates, 1)
	require.Equal(t, releaseStatusCompleted, play.trackUpdates[0].Releases[0].Status)
	require.Equal(t, []int64{42}, []int64(play.trackUpdates[0].Releases[0].VersionCodes))
	require.Len(t, play.commits, 1)
	require.Equal(t, "false", play.commits[0].Get("changesNotSentForReview"))
}

func TestDeployDryRunValidatesInsteadOfCommitting(t *testing.T) {
	play := newFakePlay(t)
	configs := testConfigs(t)
	configs.DryRun = true

	require.NoError(t, deployError(t, play, configs))

	require.Equal(t, 1, play.validates)
	require.Empty(t, play.commits)
}

func TestDeployRetriesWithoutSendingToReviewOnTheSameEdit(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true

	require.NoError(t, deployError(t, play, configs))

	require.Equal(t, 1, play.inserts, "the refused commit leaves the edit valid, it is reused")
	require.Equal(t, 1, play.uploads, "the artifact is not uploaded again")
	require.Len(t, play.trackUpdates, 1)
	require.Len(t, play.commits, 2)
	require.Equal(t, "false", play.commits[0].Get("changesNotSentForReview"))
	require.Equal(t, "true", play.commits[1].Get("changesNotSentForReview"))
}

func TestDeployFailsWithoutRetryWhenChangesCannotBeSentForReview(t *testing.T) {
	play := newFakePlay(t, commitManualReview)
	configs := testConfigs(t)

	err := deployError(t, play, configs)

	require.ErrorContains(t, err, "failed to commit edit: ")
	require.ErrorContains(t, err, changesNotSentForReviewMessage)
	require.Contains(t, hint(err), "Set retry_without_sending_to_review to true")
	require.Len(t, play.commits, 1)
}

func TestDeployReportsATrackUpdateFailureEvenWhenTracksCannotBeListed(t *testing.T) {
	play := newFakePlay(t)
	play.trackUpdateResponses = []fakeResponse{{status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Track not found.","status":"NOT_FOUND"}}`}}
	play.tracksList = &serverError
	configs := testConfigs(t)
	configs.Track = "no-such-track"

	err := deployError(t, play, configs)

	require.ErrorContains(t, err, "failed to update track")
	require.ErrorContains(t, err, "Track not found")
	require.Empty(t, play.commits)
}

func TestDeployReturnsAFailureReasonOnlyWhenTheUploadFails(t *testing.T) {
	versionCodeUsed := fakeResponse{status: http.StatusForbidden, body: `{"error":{"code":403,"message":"Version code 42 has already been used.","status":"PERMISSION_DENIED"}}`}

	tests := []struct {
		name              string
		uploadResponses   []fakeResponse
		wantFailureReason string
	}{
		{"upload succeeds", nil, ""},
		{"upload refused", []fakeResponse{versionCodeUsed}, "Version code 42 has already been used."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			play := newFakePlay(t)
			play.uploadResponses = tt.uploadResponses

			failureReason, _ := NewPublisher(log.NewLogger()).executeEdit(play.service(t), testConfigs(t))

			if tt.wantFailureReason == "" {
				require.Empty(t, failureReason)
			} else {
				require.Contains(t, failureReason, tt.wantFailureReason)
			}
		})
	}
}

func TestHint(t *testing.T) {
	apiError := func(code int, message string) error {
		return fmt.Errorf("failed to upload application(s): %w", &googleapi.Error{Code: code, Message: message})
	}

	require.Equal(t, changesNotSentForReviewHint, hint(fmt.Errorf("failed to commit edit: %w", &googleapi.Error{Code: 400, Message: changesNotSentForReviewMessage})))
	require.Equal(t, bundleInstallationWarningHint, hint(apiError(403, "The installation of the app bundle may be too large and trigger user warning on some devices, and this needs to be explicitly acknowledged in the request.")))
	require.Equal(t, internalServerErrorHint, hint(apiError(500, "Internal error encountered.")))
	require.Empty(t, hint(apiError(403, "Version code 42 has already been used.")))
}

func TestFailureReasonEndsWithTheHint(t *testing.T) {
	err := fmt.Errorf("failed to upload application(s): %w", &googleapi.Error{Code: 500, Message: "Internal error encountered."})

	require.Equal(t, err.Error()+"\n"+internalServerErrorHint, failureReasonOf(err))
	require.Equal(t, "no hint", failureReasonOf(errors.New("no hint")))
}

func TestGoogleErrorIsTheLastCause(t *testing.T) {
	inner := &googleapi.Error{Code: 400, Message: "This edit has expired, please create a new Edit."}
	inner.Wrap(errors.New(inner.Error()))
	err := fmt.Errorf("failed to upload application(s): %w", fmt.Errorf("failed to upload apk: %w", googleError(inner)))

	require.Equal(t, "failed to upload application(s):\n  failed to upload apk:\n    googleapi: Error 400: This edit has expired, please create a new Edit.", errorutil.FormattedError(err))
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 400, apiErr.Code)

	plain := errors.New("connection reset")
	require.Equal(t, plain, googleError(plain))
}

// deployError runs the edit and returns its error; it fails the test if a failure reason comes without an error.
func deployError(t *testing.T, play *fakePlay, configs Configs) error {
	t.Helper()
	failureReason, err := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)
	if err == nil {
		require.Empty(t, failureReason)
	}
	return err
}
