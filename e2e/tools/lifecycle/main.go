// Command lifecycle prints the state of every track of a Google Play app, so the e2e tests can show what a step run
// changed on Google Play. Each track is shown twice: as the edits API reports it (release status draft, inProgress,
// halted or completed) and as the applications.tracks.releases endpoint reports it (review lifecycle DRAFT,
// NOT_SENT_FOR_REVIEW, IN_REVIEW, APPROVED_NOT_PUBLISHED, NOT_APPROVED or PUBLISHED). The vendored API client predates
// the lifecycle endpoint, so both views are read through the REST API directly.
//
// Usage:
//
//	lifecycle -package <package name> -key <service account JSON path or URL> [-label <heading>]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/androidpublisher/v3"
)

const defaultBaseURL = "https://androidpublisher.googleapis.com/androidpublisher/v3"

type track struct {
	Track    string         `json:"track"`
	Releases []trackRelease `json:"releases"`
}

type trackRelease struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	UserFraction float64  `json:"userFraction"`
	VersionCodes []string `json:"versionCodes"`
}

type releaseSummary struct {
	ReleaseName           string            `json:"releaseName"`
	Track                 string            `json:"track"`
	ReleaseLifecycleState string            `json:"releaseLifecycleState"`
	ActiveArtifacts       []artifactSummary `json:"activeArtifacts"`
}

// artifactSummary keeps the version code raw: the REST API encodes int64 as a JSON string, and this tolerates both.
type artifactSummary struct {
	VersionCode json.RawMessage `json:"versionCode"`
}

func (a artifactSummary) code() string {
	return strings.Trim(string(a.VersionCode), `"`)
}

type report struct {
	Package   string
	Tracks    []track
	Lifecycle map[string][]releaseSummary
	Errors    map[string]error
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

func main() {
	pkg := flag.String("package", "", "package name of the app")
	keySource := flag.String("key", "", "service account JSON key: a file path, a file:// URL or an http(s) URL")
	label := flag.String("label", "", "heading printed above the report, for example 'before the step'")
	tracksFlag := flag.String("tracks", "", "comma separated track names to read the review lifecycle of (default: every track); the endpoint's quota is tight")
	baseURL := flag.String("base-url", defaultBaseURL, "Android Publisher API base URL")
	flag.Parse()

	if *pkg == "" || *keySource == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	client, err := newClient(ctx, *keySource)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	r, err := inspect(ctx, client, *baseURL, *pkg, splitList(*tracksFlag))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printReport(os.Stdout, *label, r)
}

func newClient(ctx context.Context, keySource string) (*http.Client, error) {
	key, err := readKey(ctx, keySource)
	if err != nil {
		return nil, fmt.Errorf("read service account key: %w", err)
	}
	cfg, err := google.JWTConfigFromJSON(key, androidpublisher.AndroidpublisherScope)
	if err != nil {
		return nil, fmt.Errorf("parse service account key: %w", err)
	}
	client := cfg.Client(ctx)
	client.Timeout = 2 * time.Minute
	return client, nil
}

func readKey(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
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
	return os.ReadFile(strings.TrimPrefix(source, "file://"))
}

func splitList(list string) []string {
	var items []string
	for _, item := range strings.Split(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// inspect opens a throwaway edit to read the tracks, deletes it, then reads the review lifecycle of the tracks in
// lifecycleTracks, or of every track when it is empty.
func inspect(ctx context.Context, client *http.Client, baseURL, pkg string, lifecycleTracks []string) (*report, error) {
	appURL := baseURL + "/applications/" + url.PathEscape(pkg)

	var edit struct {
		ID string `json:"id"`
	}
	if err := call(ctx, client, http.MethodPost, appURL+"/edits", strings.NewReader("{}"), &edit); err != nil {
		return nil, fmt.Errorf("insert edit: %w", err)
	}
	defer func() {
		if err := call(ctx, client, http.MethodDelete, appURL+"/edits/"+url.PathEscape(edit.ID), nil, nil); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "warning: could not delete the inspection edit %s: %v\n", edit.ID, err)
		}
	}()

	var tracks struct {
		Tracks []track `json:"tracks"`
	}
	if err := call(ctx, client, http.MethodGet, appURL+"/edits/"+url.PathEscape(edit.ID)+"/tracks", nil, &tracks); err != nil {
		return nil, fmt.Errorf("list tracks: %w", err)
	}

	r := &report{
		Package:   pkg,
		Tracks:    tracks.Tracks,
		Lifecycle: map[string][]releaseSummary{},
		Errors:    map[string]error{},
	}
	wanted := map[string]bool{}
	for _, name := range lifecycleTracks {
		wanted[name] = true
	}
	for _, t := range tracks.Tracks {
		if len(wanted) > 0 && !wanted[t.Track] {
			continue
		}
		releases, err := listLifecycle(ctx, client, appURL, t.Track)
		if err != nil {
			r.Errors[t.Track] = err
			continue
		}
		r.Lifecycle[t.Track] = releases
	}
	return r, nil
}

func listLifecycle(ctx context.Context, client *http.Client, appURL, trackName string) ([]releaseSummary, error) {
	var all []releaseSummary
	pageToken := ""
	for {
		u := appURL + "/tracks/" + url.PathEscape(trackName) + "/releases"
		if pageToken != "" {
			u += "?pageToken=" + url.QueryEscape(pageToken)
		}
		var page struct {
			Releases      []releaseSummary `json:"releases"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := callWithQuotaBackoff(ctx, client, u, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Releases...)
		if page.NextPageToken == "" {
			return all, nil
		}
		pageToken = page.NextPageToken
	}
}

// quotaBackoff is how long to wait between attempts when Google answers "Listing releases quota exceeded"; the
// quota of applications.tracks.releases is undocumented and was exhausted by a handful of calls in one CI build.
var quotaBackoff = 20 * time.Second

const quotaAttempts = 4

func callWithQuotaBackoff(ctx context.Context, client *http.Client, rawURL string, out interface{}) error {
	var err error
	for attempt := 1; attempt <= quotaAttempts; attempt++ {
		err = call(ctx, client, http.MethodGet, rawURL, nil, out)
		var apiErr *apiError
		if err == nil || !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "quota exceeded") || attempt == quotaAttempts {
			return err
		}
		_, _ = fmt.Fprintf(os.Stderr, "quota exceeded for %s, retrying in %s (%d/%d)\n", rawURL, quotaBackoff, attempt, quotaAttempts)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(quotaBackoff):
		}
	}
	return err
}

func call(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		return &apiError{Status: resp.StatusCode, Body: msg}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func printReport(w io.Writer, label string, r *report) {
	if label == "" {
		label = "state"
	}
	_, _ = fmt.Fprintf(w, "\n== Play state: %s (%s) ==\n", label, r.Package)

	tracks := append([]track(nil), r.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Track < tracks[j].Track })

	_, _ = fmt.Fprintln(w, "\n-- track releases (edits.tracks) --")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TRACK\tSTATUS\tVERSION CODES\tFRACTION\tRELEASE NAME")
	for _, t := range tracks {
		if len(t.Releases) == 0 {
			_, _ = fmt.Fprintf(tw, "%s\t(no releases)\t\t\t\n", t.Track)
			continue
		}
		for _, rel := range t.Releases {
			fraction := ""
			if rel.UserFraction != 0 {
				fraction = fmt.Sprintf("%g", rel.UserFraction)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Track, rel.Status, strings.Join(rel.VersionCodes, ","), fraction, rel.Name)
		}
	}
	_ = tw.Flush()

	_, _ = fmt.Fprintln(w, "\n-- review lifecycle (applications.tracks.releases) --")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TRACK\tLIFECYCLE\tVERSION CODES\tRELEASE NAME")
	for _, t := range tracks {
		if err, failed := r.Errors[t.Track]; failed {
			_, _ = fmt.Fprintf(tw, "%s\terror: %v\t\t\n", t.Track, err)
			continue
		}
		releases := r.Lifecycle[t.Track]
		if len(releases) == 0 {
			_, _ = fmt.Fprintf(tw, "%s\t(none)\t\t\n", t.Track)
			continue
		}
		for _, rel := range releases {
			codes := make([]string, 0, len(rel.ActiveArtifacts))
			for _, a := range rel.ActiveArtifacts {
				codes = append(codes, a.code())
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Track, rel.ReleaseLifecycleState, strings.Join(codes, ","), rel.ReleaseName)
		}
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
}
