// Command releasestatus checks the status of the release that holds a version code on a Google Play track, so an e2e
// test can verify what a Step run left on Google Play. It opens a throwaway edit to read the track and deletes it.
//
// Usage:
//
//	releasestatus -package <package name> -key <service account JSON path or URL> -track <track> -version-code <code> -want <status>
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
)

func main() {
	pkg := flag.String("package", "", "package name of the app")
	key := flag.String("key", "", "service account JSON key, a path or an http(s) URL")
	trackName := flag.String("track", "", "track to read")
	versionCode := flag.Int64("version-code", 0, "version code of the release to check")
	want := flag.String("want", "", "expected release status: draft, inProgress, halted or completed")
	flag.Parse()
	if *pkg == "" || *key == "" || *trackName == "" || *versionCode == 0 || *want == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	service, err := newService(ctx, *key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	track, err := readTrack(service, *pkg, *trackName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := check(track, *versionCode, *want); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("The release with version code %d on the %s track is %s.\n", *versionCode, *trackName, *want)
}

func newService(ctx context.Context, keySource string) (*androidpublisher.Service, error) {
	key, err := readKey(ctx, keySource)
	if err != nil {
		return nil, fmt.Errorf("failed to read the service account key: %w", err)
	}
	cfg, err := google.JWTConfigFromJSON(key, androidpublisher.AndroidpublisherScope)
	if err != nil {
		return nil, fmt.Errorf("failed to parse the service account key: %w", err)
	}
	return androidpublisher.NewService(ctx, option.WithHTTPClient(cfg.Client(ctx)))
}

func readKey(ctx context.Context, source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return os.ReadFile(strings.TrimPrefix(source, "file://"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// readTrack reads the track in a throwaway edit, which it deletes afterwards.
func readTrack(service *androidpublisher.Service, pkg, trackName string) (*androidpublisher.Track, error) {
	edit, err := service.Edits.Insert(pkg, &androidpublisher.AppEdit{}).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create an edit: %w", err)
	}
	defer func() { _ = service.Edits.Delete(pkg, edit.Id).Do() }()

	track, err := service.Edits.Tracks.Get(pkg, edit.Id, trackName).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to read the %s track: %w", trackName, err)
	}
	return track, nil
}

// check prints the track's releases and reports whether the release holding versionCode has the wanted status.
func check(track *androidpublisher.Track, versionCode int64, want string) error {
	fmt.Printf("Releases on the %s track:\n", track.Track)
	var found *androidpublisher.TrackRelease
	for _, release := range track.Releases {
		fmt.Printf("  %s: %q, version codes %v\n", release.Status, release.Name, release.VersionCodes)
		if slices.Contains(release.VersionCodes, versionCode) {
			found = release
		}
	}
	if found == nil {
		return fmt.Errorf("no release on the %s track holds version code %d", track.Track, versionCode)
	}
	if found.Status != want {
		return fmt.Errorf("the release with version code %d on the %s track is %s, want %s", versionCode, track.Track, found.Status, want)
	}
	return nil
}
