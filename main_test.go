package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/androidpublisher/v3"
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
			require.Equal(t, tt.behavior == changesInReviewFail, sent, "changesInReviewBehavior is only sent for fail")
		})
	}
}

func TestCommitErrorMessage(t *testing.T) {
	t.Log("changes already in review - explains that the review was left untouched and how to proceed")
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

		msg := commitErrorMessage(err)
		require.Contains(t, msg, "Failed to commit edit, error: googleapi: Error 400: You already have changes in review.")
		require.Contains(t, msg, "left that review untouched")
		require.Contains(t, msg, "changes_in_review_behavior")
		require.NotContains(t, msg, changesNotSentForReviewMessage)
	}

	t.Log("other errors - unchanged")
	{
		err := errors.New("googleapi: Error 500: Internal error encountered., backendError")
		require.False(t, isChangesAlreadyInReview(err))
		require.Equal(t, "Failed to commit edit, error: googleapi: Error 500: Internal error encountered., backendError", commitErrorMessage(err))
	}
}
