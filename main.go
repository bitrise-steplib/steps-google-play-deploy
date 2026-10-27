package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-steputils/tools"
	"github.com/bitrise-io/go-utils/v2/errorutil"
	"github.com/bitrise-io/go-utils/v2/log"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const changesNotSentForReviewMessage = "Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true"
const internalServerError = "googleapi: Error 500"

const changesNotSentForReviewHint = "Google Play does not send this app's changes for review automatically, so nothing was committed. " +
	"Set 'Retry changes without sending to review' (retry_without_sending_to_review) to true to commit without sending for review, then send the changes for review from the Google Play Console."

// editLostRetries is how many times the deploy starts over from a new edit when Google Play discards the edit.
const editLostRetries = 2

// The wait before a new edit is random, so that parallel deploys using the same service account stop discarding each
// other's edits.
const (
	editLostMinWait = 10 * time.Second
	editLostMaxWait = 30 * time.Second
)

const internalServerErrorHint = "Google Play returned an internal error without details. " +
	"Create the release manually in Google Play Console, which can show the underlying error."

// Publisher handles publishing to Google Play with integrated logging
type Publisher struct {
	logger log.Logger
	sleep  func(time.Duration)
}

// NewPublisher creates a new Publisher instance with the given logger
func NewPublisher(logger log.Logger) *Publisher {
	return &Publisher{logger: logger, sleep: time.Sleep}
}

// fail logs the error with its causes on separate lines, followed by the hint for it if there is one, and exits.
func (p *Publisher) fail(err error) {
	p.logger.Errorf("%s", errorutil.FormattedError(err))
	if h := hint(err); h != "" {
		p.logger.Warnf("%s", h)
	}
	os.Exit(1)
}

// hint returns what to do about an error with a known fix, or an empty string.
func hint(err error) string {
	switch {
	case isChangesNotSentForReviewRequired(err):
		return changesNotSentForReviewHint
	case strings.Contains(err.Error(), bundleInstallationWarning):
		return bundleInstallationWarningHint
	case strings.Contains(err.Error(), internalServerError):
		return internalServerErrorHint
	}
	return ""
}

// uploadApplications uploads every application file (apk or aab) to the Google Play. Returns the version codes of
// the uploaded apps.
func (p *Publisher) uploadApplications(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) (map[int64]int, error) {
	appPaths, _ := configs.appPaths()
	mappingPaths := configs.mappingPaths()
	versionCodes := make(map[int64]int)

	var versionCodeListLog bytes.Buffer
	versionCodeListLog.WriteString("New version codes to upload: ")

	expansionFilePaths, err := configs.expansionFiles(appPaths)
	if err != nil {
		return nil, err
	}

	for appIndex, appPath := range appPaths {
		p.logger.Printf("Uploading %v %d/%d", appPath, appIndex+1, len(appPaths))
		versionCode := int64(0)
		appFile, err := os.Open(appPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open app %s: %w", appPath, err)
		}

		if strings.ToLower(filepath.Ext(appPath)) == ".aab" {
			bundle, err := p.uploadAppBundle(service, configs.PackageName, appEdit.Id, appFile, configs.AckBundleInstallationWarning)
			if err != nil {
				return nil, err
			}
			versionCode = bundle.VersionCode
		} else {
			apk, err := p.uploadAppApk(service, configs.PackageName, appEdit.Id, appFile)
			if err != nil {
				return nil, err
			}
			versionCode = apk.VersionCode

			if len(expansionFilePaths) > 0 {
				if err := p.uploadExpansionFiles(service, expansionFilePaths[appIndex], configs.PackageName, appEdit.Id, versionCode); err != nil {
					return nil, err
				}
			}
		}

		// Upload mapping.txt files
		if len(mappingPaths)-1 >= appIndex && versionCode != 0 {
			filePath := mappingPaths[appIndex]
			if err := p.uploadMappingFile(service, appEdit.Id, versionCode, configs.PackageName, filePath); err != nil {
				return nil, err
			}
			if appIndex < len(appPaths)-1 {
				fmt.Println()
			}
		}

		versionCodes[versionCode]++
		fmt.Fprintf(&versionCodeListLog, "%d", versionCode)
		if appIndex < len(appPaths)-1 {
			versionCodeListLog.WriteString(", ")
		}
	}
	p.logger.Printf("Done uploading of %v apps", len(appPaths))
	p.logger.Printf(versionCodeListLog.String())
	return versionCodes, nil
}

// updateTracks puts the release on the configured track. When Google Play refuses it, the tracks it knows are listed,
// as a track name that does not exist is the most common cause.
func (p *Publisher) updateTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, release *androidpublisher.TrackRelease) error {
	editsTracksService := androidpublisher.NewEditsTracksService(service)

	// Note we get error if we creating multiple instances of a release with the Completed status.
	// Example: "error: googleapi: Error 400: Too many completed releases specified., releasesTooManyCompletedReleases".
	// Also receiving error when deploying a Completed release when a rollout is in progress:
	// error: googleapi: Error 403: You cannot rollout this release because it does not allow any existing users to upgrade
	// to the newly added APKs., ReleaseValidationErrorKeyApkNoUpgradePaths

	// inProgress preserves complete release even if not specified in releases array.
	// In case only a completed release specified, it halts inProgress releases.

	p.logger.Infof("%s track will be updated.", configs.Track)
	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, &androidpublisher.Track{
		Track:    configs.Track,
		Releases: []*androidpublisher.TrackRelease{release},
	})
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		if !isEditLost(err) {
			fmt.Println()
			p.logger.Infof("Available tracks on Google Play:")
			p.listTracks(configs, service, appEdit)
			p.logger.Println()
		}

		return googleError(err)
	}

	p.logger.Printf(" updated track: %s", track.Track)
	return nil
}

// listTracks lists the available tracks for an app
func (p *Publisher) listTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) {
	editsTracksService := androidpublisher.NewEditsTracksService(service)
	listTracksCall := editsTracksService.List(configs.PackageName, appEdit.Id)

	tracks, err := listTracksCall.Do()
	if err != nil {
		p.logger.Warnf("Failed to list tracks: %s", err)
		return
	}

	for _, track := range tracks.Tracks {
		p.logger.Printf("- %s", track.Track)
	}
}

func (p *Publisher) versionCodeMapToSlice(codeMap map[int64]int) []int64 {
	var versionCodes []int64
	for code, numArtifacts := range codeMap {
		if numArtifacts > 1 {
			p.logger.Warnf("There were %d artifacts uploaded for version code %d. Duplicate version codes could cause unexpected results.", numArtifacts, code)
		}
		versionCodes = append(versionCodes, code)
	}

	return versionCodes
}

func main() {
	// Initialize logger and publisher
	logger := log.NewLogger()
	publisher := NewPublisher(logger)

	// Getting configs
	fmt.Println()
	logger.Infof("Getting configuration")
	var configs Configs
	if err := stepconf.Parse(&configs); err != nil {
		publisher.fail(fmt.Errorf("failed to read inputs: %w", err))
	}
	stepconf.Print(configs)
	logger = log.NewLogger(log.WithDebugLog(configs.IsDebugLog))
	publisher = NewPublisher(logger)
	configs.Logger = logger
	if err := configs.validate(); err != nil {
		publisher.fail(err)
	}
	logger.Donef("Configuration read successfully")

	//
	// Create client and service
	fmt.Println()
	logger.Infof("Authenticating")
	client, err := publisher.createHTTPClient(string(configs.JSONKeyPath))
	if err != nil {
		publisher.fail(fmt.Errorf("failed to authenticate: %w", err))
	}
	service, err := androidpublisher.NewService(context.TODO(), option.WithHTTPClient(client))
	if err != nil {
		publisher.fail(fmt.Errorf("failed to create Google Play client: %w", err))
	}
	logger.Donef("Authenticated client created")

	//
	// Deploy
	err = publisher.deploy(service, configs, editLostRetries)

	//
	// Export outputs
	if reason := failureReason(err); reason != "" {
		fmt.Println()
		logger.Infof("Export outputs")
		if exportErr := tools.ExportEnvironmentWithEnvman("FAILURE_REASON", reason); exportErr != nil {
			logger.Warnf("Failed to export FAILURE_REASON: %s", exportErr)
		} else {
			logger.Donef("Failure reason exported")
		}
	}

	//
	// Report the result
	if err != nil {
		publisher.fail(err)
	}
}

// deploy runs the edit, and starts over from a new edit up to retryNum times if Google Play discarded the edit before
// the commit (another edit opened with the same service account, or a Play Console change).
func (p *Publisher) deploy(service *androidpublisher.Service, configs Configs, retryNum int) error {
	err := p.executeEdit(service, configs)
	for retry := 1; retry <= retryNum && isEditLost(err); retry++ {
		wait := editLostMinWait + rand.N(editLostMaxWait-editLostMinWait)
		p.logger.Warnf("%s", errorutil.FormattedError(err))
		p.logger.Warnf("Google Play discarded the edit before it was committed. This can happen if another deploy uses the same service account at the same time, or if you change the app in Google Play Console during the deploy. Starting over with a new edit in %s (retry %d of %d).", wait.Round(time.Second), retry, retryNum)
		p.sleep(wait)
		err = p.executeEdit(service, configs)
	}
	return err
}

// executeEdit runs one edit: create, upload, update the track, then validate (dry run) or commit.
func (p *Publisher) executeEdit(service *androidpublisher.Service, configs Configs) error {
	editsService := androidpublisher.NewEditsService(service)

	appEdit, err := p.createEdit(editsService, configs)
	if err != nil {
		return err
	}

	versionCodes, err := p.uploadArtifacts(configs, service, appEdit)
	if err != nil {
		return uploadError{err: err}
	}

	if strings.TrimSpace(configs.Track) == "" {
		p.logger.Infof("Skipping track update")
	} else if err := p.updateTrack(configs, service, appEdit, p.versionCodeMapToSlice(versionCodes)); err != nil {
		return fmt.Errorf("failed to update track: %w", err)
	}

	if configs.DryRun {
		return p.validateEdit(editsService, configs, appEdit)
	}
	return p.commitWithRetries(editsService, configs, appEdit)
}

func (p *Publisher) createEdit(editsService *androidpublisher.EditsService, configs Configs) (*androidpublisher.AppEdit, error) {
	fmt.Println()
	p.logger.Infof("Create new edit")
	appEdit, err := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{}).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create edit: %w", googleError(err))
	}
	p.logger.Printf(" editID: %s", appEdit.Id)
	p.logger.Donef("Edit insert created")
	return appEdit, nil
}

func (p *Publisher) uploadArtifacts(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) (map[int64]int, error) {
	fmt.Println()
	p.logger.Infof("Upload apks or app bundles")
	versionCodes, err := p.uploadApplications(configs, service, appEdit)
	if err != nil {
		return nil, err
	}
	p.logger.Donef("Applications uploaded")
	return versionCodes, nil
}

func (p *Publisher) updateTrack(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) error {
	fmt.Println()
	p.logger.Infof("Update track")
	release, err := p.createTrackRelease(configs, versionCodes)
	if err != nil {
		return err
	}
	if err := p.updateTracks(configs, service, appEdit, release); err != nil {
		return err
	}
	p.logger.Donef("Track updated")
	return nil
}

func (p *Publisher) validateEdit(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit) error {
	fmt.Println()
	p.logger.Infof("Dry run: validating edit without committing")
	if _, err := editsService.Validate(configs.PackageName, appEdit.Id).Do(); err != nil {
		return fmt.Errorf("failed to validate edit: %w", googleError(err))
	}
	p.logger.Donef("Edit validated")
	return nil
}

// commitWithRetries commits the edit. If Google Play cannot send the changes for review automatically, it commits the
// same edit once more without sending for review (retry_without_sending_to_review); a refused commit leaves the edit
// valid.
func (p *Publisher) commitWithRetries(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit) error {
	err := p.commitEdit(editsService, configs, appEdit, false)
	if isChangesNotSentForReviewRequired(err) && configs.RetryWithoutSendingToReview {
		p.logger.Warnf("%s", errorutil.FormattedError(err))
		p.logger.Warnf("Google Play does not send this app's changes for review automatically. Committing again without sending for review; send the changes for review from the Google Play Console once the Step finishes.")
		err = p.commitEdit(editsService, configs, appEdit, true)
	}
	return err
}

func (p *Publisher) commitEdit(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit, changesNotSentForReview bool) error {
	fmt.Println()
	p.logger.Infof("Committing edit")
	call := editsService.Commit(configs.PackageName, appEdit.Id)
	call.ChangesNotSentForReview(changesNotSentForReview)
	if _, err := call.Do(); err != nil {
		return fmt.Errorf("failed to commit edit: %w", googleError(err))
	}
	p.logger.Donef("Edit committed")
	return nil
}

// uploadError is an error from uploading the apps. The Step exports it as FAILURE_REASON.
type uploadError struct {
	err error
}

func (e uploadError) Error() string { return "failed to upload apps: " + e.err.Error() }

func (e uploadError) Unwrap() error { return e.err }

// failureReason is the FAILURE_REASON output for a deploy error: the upload error followed by the hint for it if
// there is one, or an empty string if the upload did not fail.
func failureReason(err error) string {
	var uploadErr uploadError
	if !errors.As(err, &uploadErr) {
		return ""
	}
	if h := hint(uploadErr.err); h != "" {
		return uploadErr.err.Error() + "\n" + h
	}
	return uploadErr.err.Error()
}

// apiError is a Google API error as the last cause of an error chain. *googleapi.Error unwraps to an error with the
// same text, which errorutil.FormattedError would print as an empty line.
type apiError struct {
	err *googleapi.Error
}

func (e apiError) Error() string { return e.err.Error() }

// As lets errors.As find the *googleapi.Error.
func (e apiError) As(target any) bool {
	if t, ok := target.(**googleapi.Error); ok {
		*t = e.err
		return true
	}
	return false
}

// googleError ends the error chain at the Google API error, if err is one.
func googleError(err error) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiError{err: apiErr}
	}
	return err
}

// googleMessage returns Google Play's own message for an API error, or the whole error otherwise.
func googleMessage(err error) string {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}

// isChangesNotSentForReviewRequired reports whether the commit needs changesNotSentForReview=true (after a rejection).
func isChangesNotSentForReviewRequired(err error) bool {
	return err != nil && strings.Contains(googleMessage(err), changesNotSentForReviewMessage)
}

// editLostPattern matches Google Play's messages for an edit it no longer accepts.
var editLostPattern = regexp.MustCompile(`(?i)create a new edit|edit has been deleted|edit has expired|edit not found`)

// isEditLost reports whether the edit itself is gone. It matches messages only: a 404 can also mean an unknown track.
func isEditLost(err error) bool {
	return err != nil && editLostPattern.MatchString(googleMessage(err))
}
