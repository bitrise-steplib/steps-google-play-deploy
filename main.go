package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-steputils/tools"
	"github.com/bitrise-io/go-utils/v2/log"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
)

const changesNotSentForReviewMessage = "Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true"
const internalServerError = "googleapi: Error 500"

// Publisher handles publishing to Google Play with integrated logging
type Publisher struct {
	logger log.Logger
}

// NewPublisher creates a new Publisher instance with the given logger
func NewPublisher(logger log.Logger) *Publisher {
	return &Publisher{logger: logger}
}

func (p *Publisher) failf(format string, v ...interface{}) {
	p.logger.Errorf(format, v...)
	os.Exit(1)
}

// uploadApplications uploads every application file (apk or aab) to the Google Play. Returns the version codes of
// the uploaded apps.
func (p *Publisher) uploadApplications(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) (map[int64]int, error) {
	appPaths, _ := configs.appPaths(p)
	mappingPaths := configs.mappingPaths()
	versionCodes := make(map[int64]int)

	var versionCodeListLog bytes.Buffer
	versionCodeListLog.WriteString("New version codes to upload: ")

	expansionFilePaths, err := expansionFiles(appPaths, configs.ExpansionfilePath, p)
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
		versionCodeListLog.WriteString(fmt.Sprintf("%d", versionCode))
		if appIndex < len(appPaths)-1 {
			versionCodeListLog.WriteString(", ")
		}
	}
	p.logger.Printf("Done uploading of %v apps", len(appPaths))
	p.logger.Printf(versionCodeListLog.String())
	return versionCodes, nil
}

// updateTracks updates the given track with a new release with the given version codes.
func (p *Publisher) updateTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) error {
	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newRelease, err := p.createTrackRelease(configs, versionCodes)
	if err != nil {
		return err
	}

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
		Releases: []*androidpublisher.TrackRelease{newRelease},
	})
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
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

	//
	// Getting configs
	fmt.Println()
	logger.Infof("Getting configuration")
	var configs Configs
	if err := stepconf.Parse(&configs); err != nil {
		publisher.failf("Couldn't create config: %s\n", err)
	}
	stepconf.Print(configs)
	if err := configs.validate(publisher); err != nil {
		publisher.failf(err.Error())
	}
	logger = log.NewLogger(log.WithDebugLog(configs.IsDebugLog))
	publisher = NewPublisher(logger)
	logger.Donef("Configuration read successfully")

	//
	// Create client and service
	fmt.Println()
	logger.Infof("Authenticating")
	client, err := createHTTPClient(string(configs.JSONKeyPath), publisher)
	if err != nil {
		publisher.failf("Failed to create HTTP client: %v", err)
	}
	service, err := androidpublisher.NewService(context.TODO(), option.WithHTTPClient(client))
	if err != nil {
		publisher.failf("Failed to create publisher service, error: %s", err)
	}
	logger.Donef("Authenticated client created")

	errorString := publisher.executeEdit(service, configs, false, configs.DryRun)
	if errorString == "" {
		return
	}
	if strings.Contains(errorString, changesNotSentForReviewMessage) {
		if configs.RetryWithoutSendingToReview {
			logger.Warnf(errorString)
			logger.Warnf("Trying to commit edit with setting changesNotSentForReview to true. Please make sure to send the changes to review from Google Play Console UI.")
			errorString = publisher.executeEdit(service, configs, true, false)
			if errorString == "" {
				return
			}
		} else {
			logger.Warnf("Sending the edit to review failed. Please change \"Retry changes without sending to review\" input to true if you wish to send the changes with the changesNotSentForReview flag. Please note that in that case the review has to be manually initiated from Google Play Console UI")
		}
	}
	if strings.Contains(errorString, internalServerError) {
		logger.Warnf("Google Play API responded with an unknown error")
		logger.Warnf("Suggestion: create a release manually in Google Play Console because the UI has the capability to present the underlying error in certain cases")
	}
	publisher.failf(errorString)
}

func (p *Publisher) executeEdit(service *androidpublisher.Service, configs Configs, changesNotSentForReview bool, dryRun bool) (errorString string) {
	editsService := androidpublisher.NewEditsService(service)
	//
	// Create insert edit
	fmt.Println()
	p.logger.Infof("Create new edit")
	editsInsertCall := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{})
	appEdit, err := editsInsertCall.Do()
	if err != nil {
		return fmt.Sprintf("Failed to perform edit insert call, error: %s", err)
	}
	p.logger.Printf(" editID: %s", appEdit.Id)
	p.logger.Donef("Edit insert created")

	//
	// List tracks that are available in the Play Store
	fmt.Println()
	p.logger.Infof("Available tracks on Google Play:")
	p.listTracks(configs, service, appEdit)
	p.logger.Donef("Tracks listed")

	//
	// Upload applications
	fmt.Println()
	p.logger.Infof("Upload apks or app bundles")
	versionCodes, err := p.uploadApplications(configs, service, appEdit)
	if err != nil {
		if failureReason := tools.ExportEnvironmentWithEnvman("FAILURE_REASON", err.Error()); failureReason != nil {
			p.logger.Warnf("Unable to export failure reason")
		} else {
			p.logger.Donef("Failure reason exported")
		}
		return fmt.Sprintf("Failed to upload application(s): %v", err)
	}
	p.logger.Donef("Applications uploaded")

	// Update track
	fmt.Println()
	p.logger.Infof("Update track")
	versionCodeSlice := p.versionCodeMapToSlice(versionCodes)
	if err := p.updateTracks(configs, service, appEdit, versionCodeSlice); err != nil {
		return fmt.Sprintf("Failed to update track, reason: %v", err)
	}
	p.logger.Donef("Track updated")

	if dryRun {
		//
		// Validate edit
		fmt.Println()
		p.logger.Infof("Dry run: validating edit without committing")
		validateEditCall := editsService.Validate(configs.PackageName, appEdit.Id)
		if _, err := validateEditCall.Do(); err != nil {
			return fmt.Sprintf("Failed to validate edit, error: %s", err)
		}
		p.logger.Donef("Edit validated")
	} else {
		//
		// Commit edit
		fmt.Println()
		p.logger.Infof("Committing edit")
		editsCommitCall := editsService.Commit(configs.PackageName, appEdit.Id)
		editsCommitCall.ChangesNotSentForReview(changesNotSentForReview)
		if _, err := editsCommitCall.Do(); err != nil {
			return fmt.Sprintf("Failed to commit edit, error: %s", err)
		}
		p.logger.Donef("Edit committed")
	}
	return ""
}
