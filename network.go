package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/v2/retryhttp"
	"github.com/hashicorp/go-retryablehttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/androidpublisher/v3"
)

// createHTTPClient creates an HTTP client for the communication during the uploads.
func (p *Publisher) createHTTPClient(jsonKeyPth string) (*http.Client, error) {
	jsonKeyPth, isRemote, err := parseURI(string(jsonKeyPth))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare key path (%s), error: %s", jsonKeyPth, err)
	}

	var authConfig *jwt.Config
	var authConfErr error
	if isRemote {
		jsonContent, err := p.downloadContentWithRetry(jsonKeyPth, 3, 3*time.Second)
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

	retryClient := retryhttp.NewClient(p.logger)
	retryClient.RetryWaitMin = 2 * time.Second
	retryClient.RetryMax = 6
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			p.logger.Debugf("Received HTTP 401 (Unauthorized), retrying request...")
			return true, nil
		}

		shouldRetry, err := retryablehttp.DefaultRetryPolicy(ctx, resp, err)
		if shouldRetry && resp != nil {
			p.logger.Debugf("Retry network error: %d", resp.StatusCode)
		}

		return shouldRetry, err
	}

	refreshCtx := context.WithValue(context.Background(), oauth2.HTTPClient, retryClient.StandardClient())

	return authConfig.Client(refreshCtx), nil
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

// parseURI parses the given URI to return the path from it if it is a local file, if it is remote the bool value is
// true.
func parseURI(keyURI string) (string, bool, error) {
	jsonURL, err := url.Parse(keyURI)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse url (%s), error: %s", keyURI, err)
	}

	return strings.TrimPrefix(keyURI, "file://"), jsonURL.Scheme == "http" || jsonURL.Scheme == "https", nil
}

// downloadContentWithRetry downloads content from the given URL using a retryable HTTP client.
func (p *Publisher) downloadContentWithRetry(downloadURL string, numberOfRetries int, waitInterval time.Duration) ([]byte, error) {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryWaitMin = waitInterval
	retryClient.RetryMax = numberOfRetries
	retryClient.CheckRetry = retryablehttp.DefaultRetryPolicy

	resp, err := retryClient.Get(downloadURL)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			p.logger.Warnf("failed to close (%s) body", downloadURL)
		}
	}()

	contentBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to read received content, error: %s", err)
	}

	return contentBytes, nil
}
