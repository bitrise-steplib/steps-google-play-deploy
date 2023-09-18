package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
)

const changesNotSentForReviewMessage = "Changes cannot be sent for review automatically. Please set the query parameter changesNotSentForReview to true"
const internalServerError = "googleapi: Error 500"

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

// uploadApplications uploads every application file (apk or aab) to the Google Play. Returns the version codes of
// the uploaded apps.
func uploadApplications(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) (map[int64]int, error) {
	appPaths, _ := configs.appPaths()
	mappingPaths := configs.mappingPaths()
	versionCodes := make(map[int64]int)

	var versionCodeListLog bytes.Buffer
	versionCodeListLog.WriteString("New version codes to upload: ")

	expansionFilePaths, err := expansionFiles(appPaths, configs.ExpansionfilePath)
	if err != nil {
		return nil, err
	}

	for appIndex, appPath := range appPaths {
		log.Printf("Uploading %v %d/%d", appPath, appIndex+1, len(appPaths))
		versionCode := int64(0)
		appFile, err := os.Open(appPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open app (%s), error: %s", appPath, err)
		}

		if strings.ToLower(filepath.Ext(appPath)) == ".aab" {
			bundle, err := uploadAppBundle(service, configs.PackageName, appEdit.Id, appFile, configs.AckBundleInstallationWarning)
			if err != nil {
				return nil, err
			}
			versionCode = bundle.VersionCode
		} else {
			apk, err := uploadAppApk(service, configs.PackageName, appEdit.Id, appFile)
			if err != nil {
				return nil, err
			}
			versionCode = apk.VersionCode

			if len(expansionFilePaths) > 0 {
				if err := uploadExpansionFiles(service, expansionFilePaths[appIndex], configs.PackageName, appEdit.Id, versionCode); err != nil {
					return nil, err
				}
			}
		}

		// Upload mapping.txt files
		if len(mappingPaths)-1 >= appIndex && versionCode != 0 {
			filePath := mappingPaths[appIndex]
			if err := uploadMappingFile(service, appEdit.Id, versionCode, configs.PackageName, filePath); err != nil {
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
	log.Printf("Done uploading of %v apps", len(appPaths))
	log.Printf(versionCodeListLog.String())
	return versionCodes, nil
}

// updateTracks updates the given track with a new release with the given version codes.
func updateTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) error {
	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newRelease, err := createTrackRelease(configs, versionCodes)
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

	log.Infof("%s track will be updated.", configs.Track)
	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, &androidpublisher.Track{
		Track:    configs.Track,
		Releases: []*androidpublisher.TrackRelease{newRelease},
	})
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		return fmt.Errorf("update call failed, error: %s", err)
	}

	log.Printf(" updated track: %s", track.Track)
	return nil
}

// listTracks lists the available tracks for an app
func listTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) {
	editsTracksService := androidpublisher.NewEditsTracksService(service)
	listTracksCall := editsTracksService.List(configs.PackageName, appEdit.Id)

	tracks, err := listTracksCall.Do()
	if err != nil {
		log.Warnf("Unable to fetch track list, error: %s", err)
	}

	for _, track := range tracks.Tracks {
		log.Printf("- %s", track.Track)
	}
}

func versionCodeMapToSlice(codeMap map[int64]int) []int64 {
	var versionCodes []int64
	for code, numArtifacts := range codeMap {
		if numArtifacts > 1 {
			log.Warnf("There were %d artifacts uploaded for version code %d. Duplicate version codes could cause unexpected results.", numArtifacts, code)
		}
		versionCodes = append(versionCodes, code)
	}

	return versionCodes
}

func main() {
	//
	// Getting configs
	fmt.Println()
	log.Infof("Getting configuration")
	var configs Configs
	if err := stepconf.Parse(&configs); err != nil {
		failf("Couldn't create config: %s\n", err)
	}
	stepconf.Print(configs)
	if err := configs.validate(); err != nil {
		failf(err.Error())
	}
	log.SetEnableDebugLog(configs.IsDebugLog)
	log.Donef("Configuration read successfully")

	//
	// Create client and service
	fmt.Println()
	log.Infof("Authenticating")
	client, err := createHTTPClient(string(configs.JSONKeyPath))
	if err != nil {
		failf("Failed to create HTTP client: %v", err)
	}
	service, err := androidpublisher.NewService(context.TODO(), option.WithHTTPClient(client))
	if err != nil {
		failf("Failed to create publisher service, error: %s", err)
	}
	log.Donef("Authenticated client created")

	errorString := executeEdit(service, configs, false)
	if errorString == "" {
		return
	}
	if strings.Contains(errorString, changesNotSentForReviewMessage) {
		if configs.RetryWithoutSendingToReview {
			log.Warnf(errorString)
			log.Warnf("Trying to commit edit with setting changesNotSentForReview to true. Please make sure to send the changes to review from Google Play Console UI.")
			errorString = executeEdit(service, configs, true)
			if errorString == "" {
				return
			}
		} else {
			log.Warnf("Sending the edit to review failed. Please change \"Retry changes without sending to review\" input to true if you wish to send the changes with the changesNotSentForReview flag. Please note that in that case the review has to be manually initiated from Google Play Console UI")
		}
	}
	if strings.Contains(errorString, internalServerError) {
		log.Warnf("Google Play API responded with an unknown error")
		log.Warnf("Suggestion: create a release manually in Google Play Console because the UI has the capability to present the underlying error in certain cases")
	}
	failf(errorString)
}

func executeEdit(service *androidpublisher.Service, configs Configs, changesNotSentForReview bool) (errorString string) {
	editsService := androidpublisher.NewEditsService(service)
	//
	// Create insert edit
	fmt.Println()
	log.Infof("Create new edit")
	editsInsertCall := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{})
	appEdit, err := editsInsertCall.Do()
	if err != nil {
		return fmt.Sprintf("Failed to perform edit insert call, error: %s", err)
	}
	log.Printf(" editID: %s", appEdit.Id)
	log.Donef("Edit insert created")

	//
	// List tracks that are available in the Play Store
	fmt.Println()
	log.Infof("Available tracks on Google Play:")
	listTracks(configs, service, appEdit)
	log.Donef("Tracks listed")

	//
	// Upload applications
	fmt.Println()
	log.Infof("Upload apks or app bundles")
	versionCodes, err := uploadApplications(configs, service, appEdit)
	if err != nil {
		return fmt.Sprintf("Failed to upload application(s): %v", err)
	}
	log.Donef("Applications uploaded")

	// Update track
	fmt.Println()
	log.Infof("Update track")
	versionCodeSlice := versionCodeMapToSlice(versionCodes)
	if err := updateTracks(configs, service, appEdit, versionCodeSlice); err != nil {
		return fmt.Sprintf("Failed to update track, reason: %v", err)
	}
	log.Donef("Track updated")

	//
	// Commit edit
	fmt.Println()
	log.Infof("Committing edit")
	editsCommitCall := editsService.Commit(configs.PackageName, appEdit.Id)
	editsCommitCall.ChangesNotSentForReview(changesNotSentForReview)
	if _, err := editsCommitCall.Do(); err != nil {
		return fmt.Sprintf("Failed to commit edit, error: %s", err)
	}
	log.Donef("Edit committed")
	return ""
}
