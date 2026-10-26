package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-steputils/tools"
	"github.com/bitrise-io/go-utils/v2/log"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const changesNotSentForReviewMessage = "Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true"
const internalServerError = "googleapi: Error 500"

const changesNotSentForReviewHint = "Google Play does not send this app's changes for review automatically, so nothing was committed. " +
	"Set retry_without_sending_to_review to true to commit without sending for review, then send the changes for review from the Google Play Console."

// Publisher handles publishing to Google Play with integrated logging
type Publisher struct {
	logger              log.Logger
	exportFailureReason func(reason string) error
}

// NewPublisher creates a new Publisher instance with the given logger
func NewPublisher(logger log.Logger) *Publisher {
	return &Publisher{
		logger: logger,
		exportFailureReason: func(reason string) error {
			return tools.ExportEnvironmentWithEnvman("FAILURE_REASON", reason)
		},
	}
}

func (p *Publisher) failf(format string, v ...interface{}) {
	p.logger.Errorf(format, v...)
	os.Exit(1)
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
			return nil, fmt.Errorf("failed to open app (%s), error: %s", appPath, err)
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

		return fmt.Errorf("update call failed, error: %s", err)
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
		p.logger.Warnf("Unable to fetch track list, error: %s", err)
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
		publisher.failf("Couldn't create config: %s\n", err)
	}
	stepconf.Print(configs)
	logger = log.NewLogger(log.WithDebugLog(configs.IsDebugLog))
	publisher = NewPublisher(logger)
	configs.Logger = logger
	if err := configs.validate(); err != nil {
		publisher.failf(err.Error())
	}
	logger.Donef("Configuration read successfully")

	//
	// Create client and service
	fmt.Println()
	logger.Infof("Authenticating")
	client, err := publisher.createHTTPClient(string(configs.JSONKeyPath))
	if err != nil {
		publisher.failf("Failed to create HTTP client: %v", err)
	}
	service, err := androidpublisher.NewService(context.TODO(), option.WithHTTPClient(client))
	if err != nil {
		publisher.failf("Failed to create publisher service, error: %s", err)
	}
	logger.Donef("Authenticated client created")

	errorString := publisher.deploy(service, configs)
	if errorString == "" {
		return
	}
	if strings.Contains(errorString, internalServerError) {
		logger.Warnf("Google Play API responded with an unknown error")
		logger.Warnf("Suggestion: create a release manually in Google Play Console because the UI has the capability to present the underlying error in certain cases")
	}
	publisher.failf(errorString)
}

// deploy runs the edit, and runs it once more from a new edit if Google Play discarded the edit before the commit
// (another edit opened with the same service account, or a Play Console change).
func (p *Publisher) deploy(service *androidpublisher.Service, configs Configs) (errorString string) {
	errorString, editLost, changesNotSentForReview := p.executeEdit(service, configs, false, false)
	if editLost {
		p.logger.Warnf("%s", errorString)
		p.logger.Warnf("Google Play discarded the edit before it was committed. This can happen if another deploy uses the same service account at the same time, or if you change the app in Google Play Console during the deploy. Starting over with a new edit.")
		errorString, _, _ = p.executeEdit(service, configs, changesNotSentForReview, true)
	}
	return errorString
}

// executeEdit runs one edit: create, upload, update the track, then validate (dry run) or commit. It returns
// whether the edit was lost before the commit, and the without-review flag in use at the end. An upload failure is
// exported as FAILURE_REASON unless the edit was lost and the deploy will start over (lastAttempt is false).
func (p *Publisher) executeEdit(service *androidpublisher.Service, configs Configs, changesNotSentForReview bool, lastAttempt bool) (errorString string, editLost bool, notSentForReview bool) {
	editsService := androidpublisher.NewEditsService(service)

	appEdit, errorString := p.createEdit(editsService, configs)
	if errorString != "" {
		return errorString, false, changesNotSentForReview
	}

	versionCodes, err := p.uploadArtifacts(configs, service, appEdit)
	if err != nil {
		lost := isEditLost(err)
		if !lost || lastAttempt {
			if exportErr := p.exportFailureReason(err.Error()); exportErr != nil {
				p.logger.Warnf("Unable to export failure reason")
			} else {
				p.logger.Donef("Failure reason exported")
			}
		}
		return fmt.Sprintf("Failed to upload application(s): %v", err), lost, changesNotSentForReview
	}

	if strings.TrimSpace(configs.Track) == "" {
		p.logger.Infof("Skipping track update")
	} else if err := p.updateTrack(configs, service, appEdit, p.versionCodeMapToSlice(versionCodes)); err != nil {
		return fmt.Sprintf("Failed to update track, reason: %v", err), isEditLost(err), changesNotSentForReview
	}

	if configs.DryRun {
		errorString, editLost := p.validateEdit(editsService, configs, appEdit)
		return errorString, editLost, changesNotSentForReview
	}
	return p.commitWithRetries(editsService, configs, appEdit, changesNotSentForReview)
}

func (p *Publisher) createEdit(editsService *androidpublisher.EditsService, configs Configs) (*androidpublisher.AppEdit, string) {
	fmt.Println()
	p.logger.Infof("Create new edit")
	appEdit, err := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{}).Do()
	if err != nil {
		return nil, fmt.Sprintf("Failed to perform edit insert call, error: %s", err)
	}
	p.logger.Printf(" editID: %s", appEdit.Id)
	p.logger.Donef("Edit insert created")
	return appEdit, ""
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

func (p *Publisher) validateEdit(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit) (errorString string, editLost bool) {
	fmt.Println()
	p.logger.Infof("Dry run: validating edit without committing")
	if _, err := editsService.Validate(configs.PackageName, appEdit.Id).Do(); err != nil {
		return fmt.Sprintf("Failed to validate edit, error: %s", err), isEditLost(err)
	}
	p.logger.Donef("Edit validated")
	return "", false
}

// commitWithRetries commits the edit, and repeats a commit Google Play cannot send for review automatically once on the
// same edit without sending for review (retry_without_sending_to_review); a refused commit leaves the edit valid.
func (p *Publisher) commitWithRetries(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit, changesNotSentForReview bool) (errorString string, editLost bool, notSentForReview bool) {
	for {
		err := p.commitEdit(editsService, configs, appEdit, changesNotSentForReview)
		if err == nil {
			return "", false, changesNotSentForReview
		}

		if isChangesNotSentForReviewRequired(err) && !changesNotSentForReview {
			if !configs.RetryWithoutSendingToReview {
				return fmt.Sprintf("Failed to commit edit, error: %s\n%s", err, changesNotSentForReviewHint), false, changesNotSentForReview
			}
			p.logger.Warnf("Failed to commit edit, error: %s", err)
			p.logger.Warnf("Google Play does not send this app's changes for review automatically. Committing again without sending for review; send the changes for review from the Google Play Console once the Step finishes.")
			changesNotSentForReview = true
			continue
		}
		return fmt.Sprintf("Failed to commit edit, error: %s", err), isEditLost(err), changesNotSentForReview
	}
}

func (p *Publisher) commitEdit(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit, changesNotSentForReview bool) error {
	fmt.Println()
	p.logger.Infof("Committing edit")
	call := editsService.Commit(configs.PackageName, appEdit.Id)
	call.ChangesNotSentForReview(changesNotSentForReview)
	if _, err := call.Do(); err != nil {
		return err
	}
	p.logger.Donef("Edit committed")
	return nil
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
