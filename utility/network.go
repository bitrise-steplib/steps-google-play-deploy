package utility

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/retry"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"

	"google.golang.org/api/androidpublisher/v3"
)

// CreateHTTPClient creates an HTTP client for the communication during the uploads.
func CreateHTTPClient(jsonKeyPth string) (*http.Client, error) {
	jsonKeyPth, isRemote, err := ParseURI(string(jsonKeyPth))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare key path (%s), error: %s", jsonKeyPth, err)
	}

	var authConfig *jwt.Config
	var authConfErr error
	if isRemote {
		jsonContent, err := downloadContentWithRetry(jsonKeyPth, 3, 3)
		if err != nil {
			return nil, fmt.Errorf("failed to download json key file, error: %s", err)
		}
		authConfig, authConfErr = google.JWTConfigFromJSON(jsonContent, androidpublisher.AndroidpublisherScope)
		if authConfErr != nil {
			return nil, err
		}
	} else {
		authConfig, authConfErr = jwtConfigFromJSONKeyFile(jsonKeyPth)
		if authConfErr != nil {
			return nil, fmt.Errorf("failed to create auth config from json key file %v, error: %s", jsonKeyPth, err)
		}
	}
	return authConfig.Client(context.TODO()), nil
}

// jwtConfigFromJSONKeyFile gets the jwt config from the given file.
func jwtConfigFromJSONKeyFile(pth string) (*jwt.Config, error) {
	jsonKeyBytes, err := fileutil.ReadBytesFromFile(pth)
	if err != nil {
		return nil, err
	}

	cfg, err := google.JWTConfigFromJSON(jsonKeyBytes, androidpublisher.AndroidpublisherScope)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// ParseURI parses the given URI to return the path from it if it is a local file, if it is remote the bool value is
// true.
func ParseURI(keyURI string) (string, bool, error) {
	jsonURL, err := url.Parse(keyURI)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse url (%s), error: %s", keyURI, err)
	}

	return strings.TrimPrefix(keyURI, "file://"), jsonURL.Scheme == "http" || jsonURL.Scheme == "https", nil
}

// downloadContentWithRetry calls downloadContent method with a given number of retries and waiting interval between the retries.
func downloadContentWithRetry(downloadURL string, numberOfRetries, waitInterval uint) ([]byte, error) {
	var contentBytes []byte
	return contentBytes, retry.Times(numberOfRetries).Wait(time.Duration(waitInterval) * time.Second).Try(func(attempt uint) error {
		var err error
		contentBytes, err = downloadContent(downloadURL)
		return err
	})
}

// downloadContent opens the given url and returns the body of the response as a byte array.
func downloadContent(downloadURL string) ([]byte, error) {
	resp, err := http.Get(downloadURL)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("failed to close (%s) body", downloadURL)
		}
	}()

	contentBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to read received conent, error: %s", err)
	}

	return contentBytes, nil
}
