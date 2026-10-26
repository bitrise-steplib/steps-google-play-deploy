package main

import (
	"context"
	"encoding/json"
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
	commitEditLost     = fakeResponse{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"This edit has expired, please create a new Edit.","status":"FAILED_PRECONDITION"}}`}
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

	require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

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

	require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

	require.Equal(t, 1, play.validates)
	require.Empty(t, play.commits)
}

func TestDeployRetriesWithoutSendingToReviewOnTheSameEdit(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

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

	errorString := NewPublisher(log.NewLogger()).deploy(play.service(t), configs)

	require.Contains(t, errorString, "Failed to commit edit, error: ")
	require.Contains(t, errorString, changesNotSentForReviewMessage)
	require.Contains(t, errorString, "Set retry_without_sending_to_review to true")
	require.Len(t, play.commits, 1)
}

func TestDeployReportsATrackUpdateFailureEvenWhenTracksCannotBeListed(t *testing.T) {
	play := newFakePlay(t)
	play.trackUpdateResponses = []fakeResponse{{status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Track not found.","status":"NOT_FOUND"}}`}}
	play.tracksList = &serverError
	configs := testConfigs(t)
	configs.Track = "no-such-track"

	errorString := NewPublisher(log.NewLogger()).deploy(play.service(t), configs)

	require.Contains(t, errorString, "Failed to update track")
	require.Contains(t, errorString, "Track not found")
	require.Empty(t, play.commits)
	require.Equal(t, 1, play.inserts, "a 404 for a missing track does not start over from a new edit")
}

func TestDeployStartsANewEditWhenGooglePlayDiscardedIt(t *testing.T) {
	t.Log("discarded before the commit")
	{
		play := newFakePlay(t, commitEditLost)
		configs := testConfigs(t)

		require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

		require.Equal(t, 2, play.inserts)
		require.Equal(t, 2, play.uploads)
		require.Len(t, play.commits, 2)
		require.Equal(t, "false", play.commits[1].Get("changesNotSentForReview"))
	}

	t.Log("discarded before the track update")
	{
		play := newFakePlay(t)
		play.trackUpdateResponses = []fakeResponse{{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"This Edit has been deleted.","status":"FAILED_PRECONDITION"}}`}}
		configs := testConfigs(t)

		require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

		require.Equal(t, 2, play.inserts)
		require.Len(t, play.trackUpdates, 2)
		require.Len(t, play.commits, 1)
	}

	t.Log("discarded twice - the Step starts over once")
	{
		play := newFakePlay(t, commitEditLost, commitEditLost)
		configs := testConfigs(t)

		errorString := NewPublisher(log.NewLogger()).deploy(play.service(t), configs)

		require.Contains(t, errorString, "Failed to commit edit, error: ")
		require.Contains(t, errorString, "This edit has expired")
		require.Equal(t, 2, play.inserts)
	}
}

func TestDeployStartsANewEditWithoutReviewWhenTheRetriedEditIsLost(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitEditLost, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

	require.Equal(t, 2, play.inserts)
	require.Equal(t, 2, play.uploads)
	require.Len(t, play.commits, 3)
	require.Equal(t, "true", play.commits[2].Get("changesNotSentForReview"), "the new edit is committed without sending for review from the start")
}

func TestDeployDryRunStartsANewEditWhenGooglePlayDiscardedIt(t *testing.T) {
	play := newFakePlay(t)
	play.validateResponses = []fakeResponse{commitEditLost}
	configs := testConfigs(t)
	configs.DryRun = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).deploy(play.service(t), configs))

	require.Equal(t, 2, play.inserts)
	require.Equal(t, 2, play.validates)
	require.Empty(t, play.commits)
}

func TestIsEditLost(t *testing.T) {
	require.True(t, isEditLost(&googleapi.Error{Code: 400, Message: "This Edit has been deleted."}))
	require.True(t, isEditLost(&googleapi.Error{Code: 400, Message: "A change was made to the application outside of this Edit, please create a new edit."}))
	require.True(t, isEditLost(fmt.Errorf("failed to upload apk, error: %s", &googleapi.Error{Code: 400, Message: "This edit has expired, please create a new Edit."})), "wrapped as text")
	require.False(t, isEditLost(&googleapi.Error{Code: 404, Message: "Track not found."}), "a 404 is not enough on its own")
	require.False(t, isEditLost(&googleapi.Error{Code: 400, Message: "Too many completed releases specified."}))
	require.False(t, isEditLost(nil))
}

func TestDeployExportsFailureReasonOnlyWhenTheUploadFailsForGood(t *testing.T) {
	editDeleted := fakeResponse{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"This Edit has been deleted.","status":"FAILED_PRECONDITION"}}`}
	versionCodeUsed := fakeResponse{status: http.StatusForbidden, body: `{"error":{"code":403,"message":"Version code 42 has already been used.","status":"PERMISSION_DENIED"}}`}

	tests := []struct {
		name            string
		uploadResponses []fakeResponse
		wantExported    []string
	}{
		{"lost edit, the restart succeeds", []fakeResponse{editDeleted}, nil},
		{"lost edit twice", []fakeResponse{editDeleted, editDeleted}, []string{"This Edit has been deleted."}},
		{"upload refused", []fakeResponse{versionCodeUsed}, []string{"Version code 42 has already been used."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			play := newFakePlay(t)
			play.uploadResponses = tt.uploadResponses
			publisher := NewPublisher(log.NewLogger())
			var exported []string
			publisher.exportFailureReason = func(reason string) error {
				exported = append(exported, reason)
				return nil
			}

			publisher.deploy(play.service(t), testConfigs(t))

			require.Len(t, exported, len(tt.wantExported))
			for i, want := range tt.wantExported {
				require.Contains(t, exported[i], want)
			}
		})
	}
}
