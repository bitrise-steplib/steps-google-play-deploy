package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
