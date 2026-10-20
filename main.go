package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Values of the changes_in_review_behavior input.
const (
	changesInReviewCancelAndResubmit = "cancel_and_resubmit"
	changesInReviewFail              = "fail"
	changesInReviewUploadAsDraft     = "upload_as_draft"
)

// changesAlreadyInReviewReason is the error reason Google Play returns to a commit sent with
// changesInReviewBehavior=ERROR_IF_IN_REVIEW while an earlier submission of the app is under review.
const changesAlreadyInReviewReason = "CHANGES_ALREADY_IN_REVIEW"

const waitOrCancelAdvice = "Wait for the review to finish or cancel it in the Google Play Console, then run the Step again."

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

	errorString := publisher.executeEdit(service, configs)
	if errorString == "" {
		return
	}
	if strings.Contains(errorString, internalServerError) {
		logger.Warnf("Google Play API responded with an unknown error")
		logger.Warnf("Suggestion: create a release manually in Google Play Console because the UI has the capability to present the underlying error in certain cases")
	}
	publisher.failf(errorString)
}

// executeEdit runs one Google Play edit: it is created once, the artifacts are uploaded once, the track is updated,
// then the edit is validated (dry run) or committed. Google Play keeps the edit valid after a refused commit, so the
// retries the inputs allow (committing without sending for review, saving the release as a draft) only redo the
// track update and the commit on the same edit.
func (p *Publisher) executeEdit(service *androidpublisher.Service, configs Configs) (errorString string) {
	editsService := androidpublisher.NewEditsService(service)

	//
	// Create insert edit
	fmt.Println()
	p.logger.Infof("Create new edit")
	appEdit, err := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{}).Do()
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

	var versionCodeSlice []int64
	if strings.TrimSpace(configs.Track) == "" {
		p.logger.Infof("Skipping track update")
	} else {
		versionCodeSlice = p.versionCodeMapToSlice(versionCodes)
		if errorString := p.updateTrack(configs, service, appEdit, versionCodeSlice); errorString != "" {
			return errorString
		}
	}

	if configs.DryRun {
		return p.validateEdit(editsService, configs, appEdit)
	}
	return p.commitWithRetries(editsService, service, configs, appEdit, versionCodeSlice)
}

// updateTrack puts the release described by configs on the track, listing the available tracks when that fails.
func (p *Publisher) updateTrack(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) (errorString string) {
	fmt.Println()
	p.logger.Infof("Update track")
	if err := p.updateTracks(configs, service, appEdit, versionCodes); err != nil {
		fmt.Println()
		p.logger.Infof("Available tracks on Google Play:")
		p.listTracks(configs, service, appEdit)
		p.logger.Println()

		return fmt.Sprintf("Failed to update track, reason: %v", err)
	}
	p.logger.Donef("Track updated")
	return ""
}

func (p *Publisher) validateEdit(editsService *androidpublisher.EditsService, configs Configs, appEdit *androidpublisher.AppEdit) (errorString string) {
	fmt.Println()
	p.logger.Infof("Dry run: validating edit without committing")
	if _, err := editsService.Validate(configs.PackageName, appEdit.Id).Do(); err != nil {
		return fmt.Sprintf("Failed to validate edit, error: %s", err)
	}
	p.logger.Donef("Edit validated")
	return ""
}

// commitWithRetries commits the edit and reacts to the two refusals the inputs let it recover from, each at most
// once and on the same edit: a commit Google Play cannot send for review automatically is repeated without sending
// for review (retry_without_sending_to_review), and a commit refused because a review is in progress is repeated
// with the release saved as a draft (changes_in_review_behavior: upload_as_draft). The draft path never sends
// Google Play's cancel-and-resubmit default, so the review in progress is never cancelled by the Step.
func (p *Publisher) commitWithRetries(editsService *androidpublisher.EditsService, service *androidpublisher.Service, configs Configs, appEdit *androidpublisher.AppEdit, versionCodes []int64) (errorString string) {
	changesNotSentForReview := false
	savedAsDraft := false

	for {
		fmt.Println()
		p.logger.Infof("Committing edit")
		if configs.ChangesInReviewBehavior != changesInReviewCancelAndResubmit {
			p.logger.Printf("A review in progress is not cancelled: the commit fails if Google Play is still reviewing an earlier submission of the app.")
		}
		err := p.commitEdit(editsService, configs, appEdit.Id, changesNotSentForReview)
		if err == nil {
			p.logger.Donef("Edit committed")
			if savedAsDraft {
				p.logger.Warnf("The release was saved as a draft on the %s track because a review is in progress. Roll it out from the Google Play Console once the review is complete.", configs.Track)
			}
			return ""
		}

		switch {
		case isChangesNotSentForReviewRequired(err) && !changesNotSentForReview:
			if !configs.RetryWithoutSendingToReview {
				return fmt.Sprintf("Google Play does not send this app's changes for review automatically, so the commit was refused and nothing was committed (Google Play: %s). "+
					"Set retry_without_sending_to_review to true to commit without sending for review, then send the changes for review from the Google Play Console.", googleMessage(err))
			}
			p.logger.Warnf("Google Play does not send this app's changes for review automatically (Google Play: %s). Committing without sending for review; send the changes for review from the Google Play Console once the Step finishes.", googleMessage(err))
			changesNotSentForReview = true

		case isChangesAlreadyInReview(err) && configs.ChangesInReviewBehavior == changesInReviewUploadAsDraft && !savedAsDraft:
			if advice := draftFallbackUnavailableAdvice(configs); advice != "" {
				return reviewInProgressFailure(err, advice)
			}
			p.logger.Warnf("Google Play is still reviewing an earlier submission of this app (Google Play: %s). Saving the release as a draft on the %s track instead, without sending it for review.", googleMessage(err), configs.Track)
			draftConfigs := configs
			draftConfigs.Status = releaseStatusDraft
			if errorString := p.updateTrack(draftConfigs, service, appEdit, versionCodes); errorString != "" {
				return errorString
			}
			savedAsDraft = true

		case isChangesAlreadyInReview(err) && savedAsDraft:
			return fmt.Sprintf("Google Play refused to save the release as a draft while an earlier submission is in review, so nothing was committed and that review was left untouched (Google Play: %s). %s", googleMessage(err), waitOrCancelAdvice)

		case isChangesAlreadyInReview(err):
			return reviewInProgressFailure(err, waitOrCancelAdvice+" To replace the review with these changes instead, set changes_in_review_behavior to cancel_and_resubmit.")

		default:
			return fmt.Sprintf("Failed to commit edit: %s", err)
		}
	}
}

// reviewInProgressFailure is the Step's message when Google Play refused the commit because a review is in progress;
// advice says what to do next.
func reviewInProgressFailure(err error, advice string) string {
	return fmt.Sprintf("Google Play is still reviewing an earlier submission of this app, so nothing was committed and that review was left untouched (Google Play: %s). %s", googleMessage(err), advice)
}

// draftFallbackUnavailableAdvice says why a refused commit cannot be retried as a draft and what to do instead, or
// returns "" when the fallback is possible: the release has to exist (a track is set) and must not be a draft already.
func draftFallbackUnavailableAdvice(configs Configs) string {
	if strings.TrimSpace(configs.Track) == "" {
		return "There is no track to save a draft on: set a track to use upload_as_draft, or run the Step again once the review is complete."
	}
	if configs.Status == releaseStatusDraft {
		return "The release is a draft already, so there is nothing to fall back to: run the Step again once the review is complete, or cancel the review in the Google Play Console."
	}
	return ""
}

// googleMessage returns Google Play's own message for an API error, without the HTTP code and the details block the
// client appends, or the whole error when it is not an API error.
func googleMessage(err error) string {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}

// commitEdit commits the edit. changesNotSentForReview and the changes_in_review_behavior input map to the commit
// call's query parameters; cancel_and_resubmit is Google Play's default, so it is not sent.
func (p *Publisher) commitEdit(editsService *androidpublisher.EditsService, configs Configs, editID string, changesNotSentForReview bool) error {
	call := editsService.Commit(configs.PackageName, editID)
	call.ChangesNotSentForReview(changesNotSentForReview)
	if configs.ChangesInReviewBehavior == changesInReviewFail || configs.ChangesInReviewBehavior == changesInReviewUploadAsDraft {
		call.ChangesInReviewBehavior("ERROR_IF_IN_REVIEW")
	}
	_, err := call.Do()
	return err
}

// isChangesNotSentForReviewRequired reports whether Google Play refused the commit because the app is in the state
// where changes are not sent for review automatically (after a rejection) and the flag has to be set.
func isChangesNotSentForReviewRequired(err error) bool {
	return err != nil && strings.Contains(err.Error(), changesNotSentForReviewMessage)
}

// isChangesAlreadyInReview reports whether Google Play refused the commit because a review is in progress. The reason
// lives in the error's ErrorInfo details, which the client keeps in the raw body.
func isChangesAlreadyInReview(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Body, changesAlreadyInReviewReason) {
		return true
	}
	return strings.Contains(err.Error(), changesAlreadyInReviewReason)
}
