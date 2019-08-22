package utility

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"

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

	if isRemote {
		tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
		if err != nil {
			return nil, fmt.Errorf("failed to create tmp dir, error: %s", err)
		}

		jsonKeySource := jsonKeyPth
		jsonKeyPth = filepath.Join(tmpDir, "key.json")
		if err := downloadFile(jsonKeySource, jsonKeyPth); err != nil {
			return nil, fmt.Errorf("failed to download json key file, error: %s", err)
		}
	}

	authConfig, err := jwtConfigFromJSONKeyFile(jsonKeyPth)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth config from json key file %v, error: %s", jsonKeyPth, err)
	}
	jwtConfig := authConfig

	return jwtConfig.Client(context.TODO()), nil
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

// downloadFile downloads a file from the given URL to the given target path.
func downloadFile(downloadURL, targetPath string) error {
	outFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed to create (%s), error: %s", targetPath, err)
	}
	defer func() {
		if err := outFile.Close(); err != nil {
			log.Warnf("Failed to close (%s)", targetPath)
		}
	}()

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("failed to close (%s) body", downloadURL)
		}
	}()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}

	return nil
}
