package main

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCommitEdit(t *testing.T) {
	tests := []struct {
		name                    string
		behavior                string
		changesNotSentForReview bool
		wantQuery               url.Values
	}{
		{
			name:     "cancel_and_resubmit sends no changesInReviewBehavior, Google Play's default applies",
			behavior: changesInReviewCancelAndResubmit,
			wantQuery: url.Values{
				"changesNotSentForReview": {"false"},
			},
		},
		{
			name:     "fail sends ERROR_IF_IN_REVIEW",
			behavior: changesInReviewFail,
			wantQuery: url.Values{
				"changesNotSentForReview": {"false"},
				"changesInReviewBehavior": {"ERROR_IF_IN_REVIEW"},
			},
		},
		{
			name:     "upload_as_draft also sends ERROR_IF_IN_REVIEW, the draft path never cancels a review",
			behavior: changesInReviewUploadAsDraft,
			wantQuery: url.Values{
				"changesNotSentForReview": {"false"},
				"changesInReviewBehavior": {"ERROR_IF_IN_REVIEW"},
			},
		},
		{
			name:                    "the retry without sending to review keeps the behavior",
			behavior:                changesInReviewFail,
			changesNotSentForReview: true,
			wantQuery: url.Values{
				"changesNotSentForReview": {"true"},
				"changesInReviewBehavior": {"ERROR_IF_IN_REVIEW"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotQuery url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"edit-1"}`))
			}))
			defer server.Close()

			service, err := androidpublisher.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
			require.NoError(t, err)

			publisher := NewPublisher(log.NewLogger())
			configs := Configs{PackageName: "com.example.app", ChangesInReviewBehavior: tt.behavior}
			require.NoError(t, publisher.commitEdit(androidpublisher.NewEditsService(service), configs, "edit-1", tt.changesNotSentForReview))

			require.Equal(t, "/androidpublisher/v3/applications/com.example.app/edits/edit-1:commit", gotPath)
			for key, want := range tt.wantQuery {
				require.Equal(t, want, gotQuery[key], key)
			}
			_, sent := gotQuery["changesInReviewBehavior"]
			require.Equal(t, tt.behavior != changesInReviewCancelAndResubmit, sent, "changesInReviewBehavior is not sent for cancel_and_resubmit")
		})
	}
}

func TestReviewInProgressFailure(t *testing.T) {
	t.Log("changes already in review - names the cause, Google Play's message and what to do next")
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"You already have changes in review. Cancel this review or wait for it to complete before you try again.","status":"FAILED_PRECONDITION","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CHANGES_ALREADY_IN_REVIEW","domain":"androidpublisher.googleapis.com","metadata":{"editId":"edit-1","method":"edits.commit"}}]}}`))
		}))
		defer server.Close()

		service, err := androidpublisher.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
		require.NoError(t, err)

		publisher := NewPublisher(log.NewLogger())
		configs := Configs{PackageName: "com.example.app", ChangesInReviewBehavior: changesInReviewFail}
		err = publisher.commitEdit(androidpublisher.NewEditsService(service), configs, "edit-1", false)
		require.Error(t, err)
		require.True(t, isChangesAlreadyInReview(err))

		require.Equal(t, "You already have changes in review. Cancel this review or wait for it to complete before you try again.", googleMessage(err), "the HTTP code and the details block are left out")

		msg := reviewInProgressFailure(err, waitOrCancelAdvice)
		require.Equal(t, "Google Play is still reviewing an earlier submission of this app, so nothing was committed and that review was left untouched "+
			"(Google Play: You already have changes in review. Cancel this review or wait for it to complete before you try again.). "+
			"Wait for the review to finish or cancel it in the Google Play Console, then run the Step again.", msg)
	}

	t.Log("other errors - passed through whole")
	{
		err := errors.New("googleapi: Error 500: Internal error encountered., backendError")
		require.False(t, isChangesAlreadyInReview(err))
		require.Equal(t, "googleapi: Error 500: Internal error encountered., backendError", googleMessage(err))
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
	editBody                   = `{"id":"edit-1"}`
	changesAlreadyInReviewBody = `{"error":{"code":400,"message":"You already have changes in review. Cancel this review or wait for it to complete before you try again.","status":"FAILED_PRECONDITION","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CHANGES_ALREADY_IN_REVIEW","domain":"androidpublisher.googleapis.com","metadata":{"editId":"edit-1","method":"edits.commit"}}]}}`
	cannotBeSentForReviewBody  = `{"error":{"code":400,"message":"Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true.","status":"INVALID_ARGUMENT"}}`
)

var (
	commitOK           = fakeResponse{status: http.StatusOK, body: editBody}
	commitInReview     = fakeResponse{status: http.StatusBadRequest, body: changesAlreadyInReviewBody}
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
		PackageName:             "com.example.app",
		AppPath:                 apk,
		Track:                   "internal",
		ChangesInReviewBehavior: changesInReviewCancelAndResubmit,
		Logger:                  log.NewLogger(),
	}
}

func TestExecuteEditCommitsOnce(t *testing.T) {
	play := newFakePlay(t)
	configs := testConfigs(t)

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 1, play.inserts)
	require.Equal(t, 1, play.uploads)
	require.Len(t, play.trackUpdates, 1)
	require.Equal(t, releaseStatusCompleted, play.trackUpdates[0].Releases[0].Status)
	require.Equal(t, []int64{42}, []int64(play.trackUpdates[0].Releases[0].VersionCodes))
	require.Len(t, play.commits, 1)
	require.Equal(t, "false", play.commits[0].Get("changesNotSentForReview"))
	require.NotContains(t, play.commits[0], "changesInReviewBehavior")
}

func TestExecuteEditDryRunValidatesInsteadOfCommitting(t *testing.T) {
	play := newFakePlay(t)
	configs := testConfigs(t)
	configs.DryRun = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 1, play.validates)
	require.Empty(t, play.commits)
}

func TestExecuteEditFailsWhenReviewInProgress(t *testing.T) {
	play := newFakePlay(t, commitInReview)
	configs := testConfigs(t)
	configs.ChangesInReviewBehavior = changesInReviewFail

	errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

	require.Contains(t, errorString, "(Google Play: You already have changes in review.")
	require.Contains(t, errorString, "set changes_in_review_behavior to cancel_and_resubmit")
	require.Len(t, play.commits, 1, "fail does not retry")
	require.Len(t, play.trackUpdates, 1)
}

func TestExecuteEditSavesDraftWhenReviewInProgress(t *testing.T) {
	play := newFakePlay(t, commitInReview, commitOK)
	configs := testConfigs(t)
	configs.ChangesInReviewBehavior = changesInReviewUploadAsDraft
	configs.UserFraction = 0.5
	configs.ReleaseName = "1.2.3"

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 1, play.inserts, "the refused commit leaves the edit valid, it is reused")
	require.Equal(t, 1, play.uploads, "the artifact is not uploaded again")

	require.Len(t, play.trackUpdates, 2)
	first, second := play.trackUpdates[0].Releases[0], play.trackUpdates[1].Releases[0]
	require.Equal(t, releaseStatusInProgress, first.Status)
	require.Equal(t, 0.5, first.UserFraction)
	require.Equal(t, releaseStatusDraft, second.Status)
	require.Zero(t, second.UserFraction, "a draft has no user fraction")
	require.Equal(t, first.VersionCodes, second.VersionCodes)
	require.Equal(t, "1.2.3", second.Name)

	require.Len(t, play.commits, 2)
	for _, commit := range play.commits {
		require.Equal(t, "ERROR_IF_IN_REVIEW", commit.Get("changesInReviewBehavior"), "the draft path never asks Google Play to cancel the review")
	}
}

func TestExecuteEditFailsWhenDraftIsRefusedToo(t *testing.T) {
	play := newFakePlay(t, commitInReview, commitInReview)
	configs := testConfigs(t)
	configs.ChangesInReviewBehavior = changesInReviewUploadAsDraft

	errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

	require.Contains(t, errorString, "refused to save the release as a draft")
	require.Contains(t, errorString, waitOrCancelAdvice)
	require.Len(t, play.commits, 2, "the draft is tried once")
	require.Len(t, play.trackUpdates, 2)
}

func TestExecuteEditDoesNotFallBackToDraft(t *testing.T) {
	tests := []struct {
		name             string
		prepare          func(*Configs)
		wantTrackUpdates int
		wantAdvice       string
	}{
		{"the release is a draft already", func(c *Configs) { c.Status = releaseStatusDraft }, 1, "The release is a draft already"},
		{"no track is set", func(c *Configs) { c.Track = "" }, 0, "There is no track to save a draft on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			play := newFakePlay(t, commitInReview)
			configs := testConfigs(t)
			configs.ChangesInReviewBehavior = changesInReviewUploadAsDraft
			tt.prepare(&configs)

			errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

			require.Contains(t, errorString, "that review was left untouched")
			require.Contains(t, errorString, tt.wantAdvice)
			require.Len(t, play.commits, 1)
			require.Len(t, play.trackUpdates, tt.wantTrackUpdates)
		})
	}
}

func TestExecuteEditRetriesWithoutSendingToReviewOnTheSameEdit(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 1, play.inserts)
	require.Equal(t, 1, play.uploads)
	require.Len(t, play.trackUpdates, 1)
	require.Len(t, play.commits, 2)
	require.Equal(t, "false", play.commits[0].Get("changesNotSentForReview"))
	require.Equal(t, "true", play.commits[1].Get("changesNotSentForReview"))
}

func TestExecuteEditFailsWithoutRetryWhenChangesCannotBeSentForReview(t *testing.T) {
	play := newFakePlay(t, commitManualReview)
	configs := testConfigs(t)

	errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

	require.Contains(t, errorString, "Set retry_without_sending_to_review to true")
	require.Contains(t, errorString, "(Google Play: "+changesNotSentForReviewMessage)
	require.Len(t, play.commits, 1)
}

func TestExecuteEditRetryWithoutReviewThenDraft(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitInReview, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true
	configs.ChangesInReviewBehavior = changesInReviewUploadAsDraft

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 1, play.inserts)
	require.Len(t, play.commits, 3)
	last := play.commits[2]
	require.Equal(t, "true", last.Get("changesNotSentForReview"))
	require.Equal(t, "ERROR_IF_IN_REVIEW", last.Get("changesInReviewBehavior"))
	require.Len(t, play.trackUpdates, 2)
	require.Equal(t, releaseStatusDraft, play.trackUpdates[1].Releases[0].Status)
}

func TestExecuteEditStartsANewEditWhenTheRetriedEditIsLost(t *testing.T) {
	play := newFakePlay(t, commitManualReview, commitEditLost, commitOK)
	configs := testConfigs(t)
	configs.RetryWithoutSendingToReview = true

	require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

	require.Equal(t, 2, play.inserts, "the deploy is repeated from a new edit")
	require.Equal(t, 2, play.uploads)
	require.Len(t, play.commits, 3)
	require.Equal(t, "true", play.commits[2].Get("changesNotSentForReview"), "the new edit is committed without sending for review from the start")
}

func TestExecuteEditStartsANewEditWhenGooglePlayDiscardedIt(t *testing.T) {
	t.Log("discarded before the commit")
	{
		play := newFakePlay(t, commitEditLost)
		configs := testConfigs(t)

		require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

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

		require.Equal(t, "", NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs))

		require.Equal(t, 2, play.inserts)
		require.Len(t, play.trackUpdates, 2)
		require.Len(t, play.commits, 1)
	}

	t.Log("discarded twice - the Step starts over once")
	{
		play := newFakePlay(t, commitEditLost, commitEditLost)
		configs := testConfigs(t)

		errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

		require.Contains(t, errorString, "Failed to commit edit: ")
		require.Contains(t, errorString, "This edit has expired")
		require.Equal(t, 2, play.inserts)
	}
}

func TestExecuteEditReportsATrackUpdateFailureEvenWhenTracksCannotBeListed(t *testing.T) {
	play := newFakePlay(t)
	notFound := fakeResponse{status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Track not found.","status":"NOT_FOUND"}}`}
	play.trackUpdateResponses = []fakeResponse{notFound, notFound}
	play.tracksList = &serverError
	configs := testConfigs(t)
	configs.Track = "no-such-track"

	errorString := NewPublisher(log.NewLogger()).executeEdit(play.service(t), configs)

	require.Contains(t, errorString, "Failed to update track")
	require.Contains(t, errorString, "Track not found")
	require.Empty(t, play.commits)
}

func TestIsChangesAlreadyInReview(t *testing.T) {
	require.True(t, isChangesAlreadyInReview(&googleapi.Error{Code: 400, Message: "x", Details: []interface{}{map[string]interface{}{"reason": "CHANGES_ALREADY_IN_REVIEW"}}}))
	require.True(t, isChangesAlreadyInReview(&googleapi.Error{Code: 400, Errors: []googleapi.ErrorItem{{Reason: "CHANGES_ALREADY_IN_REVIEW"}}}))
	require.False(t, isChangesAlreadyInReview(&googleapi.Error{Code: 400, Message: "mentions CHANGES_ALREADY_IN_REVIEW only in the text"}))
	require.False(t, isChangesAlreadyInReview(errors.New("CHANGES_ALREADY_IN_REVIEW")))
}

func TestIsEditLost(t *testing.T) {
	require.True(t, isEditLost(&googleapi.Error{Code: 404, Message: "Requested entity was not found."}))
	require.True(t, isEditLost(&googleapi.Error{Code: 400, Message: "This Edit has been deleted."}))
	require.True(t, isEditLost(&googleapi.Error{Code: 400, Message: "A change was made to the application outside of this Edit, please create a new edit."}))
	require.False(t, isEditLost(&googleapi.Error{Code: 400, Message: "Too many completed releases specified."}))
	require.False(t, isEditLost(nil))
}
