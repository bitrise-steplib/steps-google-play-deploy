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
	"github.com/bitrise-steplib/steps-google-play-deploy/pairing"
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
	appPaths, _ := configs.appPaths()
	versionCodes := make(map[int64]int)

	// Pair each artifact with its own mapping file by content rather than by list
	// position. R8 stamps a pg_map_id into every artifact it minifies and writes the
	// same id into the header of the mapping file for that run, so the pairing holds
	// even though the deploy directory has flattened the paths, collision-renamed the
	// mapping files, and sign-apk has rewritten the artifacts.
	mappingPaths := configs.mappingPaths()
	mappingIndex := pairing.BuildIndex(mappingPaths, p.logger)

	// Fall back to the previous positional behaviour when no candidate carries a
	// pg_map_id at all, which means nothing can be matched by content.
	//
	// R8 always writes the id, in both full and compatibility mode, and AGP has not
	// let you swap R8 out for ProGuard since AGP 7 (removed android.enableR8) — with
	// ProGuard support dropped entirely in AGP 8. So this is not about old AGP
	// versions. It is about obfuscators that are not R8: Guardsquare's standalone
	// ProGuard Gradle plugin, which is the supported way to run ProGuard on AGP 7+,
	// and DexGuard. Their mapping files have no pg_map_id and their artifacts carry
	// no R8 marker, so positional matching is the only thing available.
	pairByContent := len(mappingIndex) > 0
	switch {
	case pairByContent:
		p.logger.Printf("Indexed %d mapping file id(s) to pair with the uploaded artifacts by content", len(mappingIndex))
	case len(mappingPaths) > 0:
		p.logger.Warnf("None of the %d mapping file(s) has a '# pg_map_id:' header, so they cannot be matched to an artifact by content.", len(mappingPaths))
		p.logger.Warnf("Falling back to matching them to the app_path list by position. This is expected from a non-R8 obfuscator such as the standalone ProGuard plugin or DexGuard; from an R8 build it usually means these are not the mapping files the build published.")
	}

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

		// Upload the mapping file belonging to this artifact.
		if versionCode != 0 && len(mappingPaths) > 0 {
			mappingPath, err := p.mappingFor(appPath, appIndex, mappingPaths, mappingIndex, pairByContent)
			if err != nil {
				// Never fail a deploy that has already succeeded over a mapping file.
				p.logger.Warnf("Could not determine the mapping file for %s: %s", filepath.Base(appPath), err)
			} else if mappingPath != "" {
				if err := p.uploadMappingFile(service, appEdit.Id, versionCode, configs.PackageName, mappingPath); err != nil {
					return nil, err
				}
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

// mappingFor returns the mapping file to upload for the given app artifact, or ""
// when there is none to upload.
//
// Content pairing is used whenever the candidates carry pg_map_ids; the positional
// fallback exists only for mapping files that have no id to match on.
func (p Publisher) mappingFor(appPath string, appIndex int, mappingPaths []string, index pairing.Index, pairByContent bool) (string, error) {
	if !pairByContent {
		if appIndex >= len(mappingPaths) {
			return "", nil
		}
		return mappingPaths[appIndex], nil
	}

	mappingPath, needsMapping, err := index.ForArtifact(appPath, p.logger)
	switch {
	case err != nil:
		return "", err
	case !needsMapping:
		// Either not minified, or ambiguous; ForArtifact has already logged which.
		return "", nil
	case mappingPath == "":
		p.logger.Warnf("  %s is minified but none of the %d mapping file(s) provided matches it. Crash reports for this version will not be deobfuscated.", filepath.Base(appPath), len(mappingPaths))
		return "", nil
	}
	return mappingPath, nil
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

	if strings.TrimSpace(configs.Track) == "" {
		p.logger.Infof("Skipping track update")
	} else {
		// Update track
		fmt.Println()
		p.logger.Infof("Update track")
		versionCodeSlice := p.versionCodeMapToSlice(versionCodes)
		if err := p.updateTracks(configs, service, appEdit, versionCodeSlice); err != nil {
			fmt.Println()
			p.logger.Infof("Available tracks on Google Play:")
			p.listTracks(configs, service, appEdit)
			p.logger.Println()

			return fmt.Sprintf("Failed to update track, reason: %v", err)
		}
		p.logger.Donef("Track updated")
	}

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
