// Command discard-edit opens a Google Play edit for a package with the given service account and leaves it open.
// Google Play keeps one open edit per service account, so this discards the edit another process holds, the way a
// parallel deploy does. The e2e tests use it to make the Step lose its edit mid-deploy.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
)

func main() {
	pkg := flag.String("package", "", "package name of the app")
	keySource := flag.String("key", "", "service account JSON key: a file path, a file:// URL or an http(s) URL")
	flag.Parse()
	if *pkg == "" || *keySource == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*pkg, *keySource); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(pkg, keySource string) error {
	ctx := context.Background()
	key, err := readKey(keySource)
	if err != nil {
		return fmt.Errorf("read service account key: %w", err)
	}
	cfg, err := google.JWTConfigFromJSON(key, androidpublisher.AndroidpublisherScope)
	if err != nil {
		return fmt.Errorf("parse service account key: %w", err)
	}
	service, err := androidpublisher.NewService(ctx, option.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return err
	}
	edit, err := service.Edits.Insert(pkg, &androidpublisher.AppEdit{}).Do()
	if err != nil {
		return fmt.Errorf("insert edit: %w", err)
	}
	fmt.Printf("discard-edit: opened edit %s, the previously open edit of this service account is discarded\n", edit.Id)
	return nil
}

func readKey(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := http.Get(source) // #nosec G107 -- the URL is the e2e secret
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(strings.TrimPrefix(source, "file://"))
}
