package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-steplib/steps-google-play-deploy/config"
	"github.com/bitrise-steplib/steps-google-play-deploy/utility"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
)

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

// uploadApplications uploads every application file (apk or aab) to the Google Play. Returns the version codes of
// the uploaded apps.
func uploadApplications(configs config.Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) ([]int64, error) {
	var versionCodes []int64
	appPaths, _ := configs.AppPaths()

	expansionFileUpload, expansionFilePaths, err := utility.GetExpansionFiles(appPaths, configs.ExpansionfilePath)
	if err != nil {
		return []int64{}, err
	}

	for i, appPath := range appPaths {
		log.Printf("Uploading %v", appPath)
		versionCode := int64(0)
		appFile, err := os.Open(appPath)
		if err != nil {
			return []int64{}, fmt.Errorf("failed to open app (%s), error: %s", appPath, err)
		}

		if strings.ToLower(filepath.Ext(appPath)) == ".aab" {
			bundle, err := utility.UploadAppBundle(service, configs.PackageName, appEdit.Id, appFile)
			if err != nil {
				return []int64{}, err
			}
			versionCodes = append(versionCodes, bundle.VersionCode)
			versionCode = bundle.VersionCode
		} else {
			apk, err := utility.UploadAppApk(service, configs.PackageName, appEdit.Id, appFile)
			if err != nil {
				return []int64{}, err
			}
			versionCodes = append(versionCodes, apk.VersionCode)
			versionCode = apk.VersionCode

			if expansionFileUpload {
				if err := utility.UploadExpansionFiles(service, expansionFilePaths[i], configs.PackageName, appEdit.Id, versionCode); err != nil {
					return []int64{}, err
				}
			}
		}

		// Upload mapping.txt
		if configs.MappingFile != "" && versionCode != 0 {
			if err := utility.UploadMappingFile(service, configs, appEdit.Id, versionCode); err != nil {
				return []int64{}, err
			}
			if i < len(appPaths)-1 {
				fmt.Println()
			}
		}
	}
	log.Printf("Done uploading of %v apps", len(appPaths))
	log.Printf("New version codes to upload: %v", versionCodes)
	return versionCodes, nil
}

// updateTrack updates the given track with a new release with the given version codes.
func updateTrack(configs config.Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) error {
	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newTrack, err := utility.GetTrack(configs, service, appEdit, configs.Track)
	if err != nil {
		return err
	}

	newRelease, err := utility.GetNewRelease(configs, versionCodes)
	if err != nil {
		return err
	}
	newTrack.Releases = append(newTrack.Releases, &newRelease)
	utility.PrintTrack(newTrack, "New track to upload:")

	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, newTrack)
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		return fmt.Errorf("update call failed, error: %s", err)
	}

	log.Printf(" updated track: %s", track.Track)
	log.Printf(" assigned apk versions: %v", newRelease.VersionCodes)
	return nil
}

func main() {
	//
	// Getting configs
	fmt.Println()
	log.Infof("Getting configuration")
	var configs config.Configs
	if err := stepconf.Parse(&configs); err != nil {
		failf("Couldn't create config: %s\n", err)
	}
	stepconf.Print(configs)
	if err := configs.Validate(); err != nil {
		failf(err.Error())
	}
	log.Donef("Configuration read successfully")

	//
	// Create client and service
	fmt.Println()
	log.Infof("Authenticating")
	client, err := utility.CreateHTTPClient(string(configs.JSONKeyPath))
	if err != nil {
		failf("Failed to create HTTP client: %v", err)
	}
	service, err := androidpublisher.NewService(context.TODO(), option.WithHTTPClient(client))
	if err != nil {
		failf("Failed to create publisher service, error: %s", err)
	}
	log.Donef("Authenticated client created")
	// ---

	//
	// Create insert edit
	fmt.Println()
	log.Infof("Create new edit")
	editsService := androidpublisher.NewEditsService(service)
	editsInsertCall := editsService.Insert(configs.PackageName, nil)
	appEdit, err := editsInsertCall.Do()
	if err != nil {
		failf("Failed to perform edit insert call, error: %s", err)
	}
	log.Printf(" editID: %s", appEdit.Id)
	log.Donef("Edit insert created")
	// ---

	//
	// Upload applications
	fmt.Println()
	log.Infof("Upload apks or app bundles")
	versionCodes, err := uploadApplications(configs, service, appEdit)
	if err != nil {
		failf("Failed to upload APKs: %v", err)
	}
	log.Donef("Applications uploaded")
	// ---

	// Update track
	fmt.Println()
	log.Infof("Update track")
	if err := updateTrack(configs, service, appEdit, versionCodes); err != nil {
		failf("Failed to update track, reason: %v", err)
	}
	log.Donef("Track updated")
	// ---

	//
	// Validate edit
	fmt.Println()
	log.Infof("Validating edit")
	editsValidateCall := editsService.Validate(configs.PackageName, appEdit.Id)
	if _, err := editsValidateCall.Do(); err != nil {
		failf("Failed to validate edit, error: %s", err)
	}
	log.Donef("Edit is valid")
	// ---

	//
	// Commit edit
	fmt.Println()
	log.Infof("Committing edit")
	editsCommitCall := editsService.Commit(configs.PackageName, appEdit.Id)
	if _, err := editsCommitCall.Do(); err != nil {
		failf("Failed to commit edit, error: %s", err)
	}

	log.Donef("Edit committed")
	// ---
}
