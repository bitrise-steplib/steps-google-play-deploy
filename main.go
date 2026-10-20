package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
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
		fmt.Println()
		p.logger.Infof("Available tracks on Google Play:")
		p.listTracks(configs, service, appEdit)
		p.logger.Println()

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

// executeEdit deploys through one Google Play edit. Google Play keeps an edit valid after a refused commit, so the
// retries the inputs allow are made on the same edit; should Google Play nevertheless discard the edit on the retry
// without sending for review, the deploy is repeated once from a new edit with that flag set from the start, which
// is what earlier versions of the Step always did.
func (p *Publisher) executeEdit(service *androidpublisher.Service, configs Configs) (errorString string) {
	errorString, editLost := p.runEdit(service, configs, false)
	if editLost {
		p.logger.Warnf("Google Play discarded the edit after the refused commit. Starting a new edit and committing it without sending for review.")
		errorString, _ = p.runEdit(service, configs, true)
	}
	return errorString
}

// runEdit creates the edit, uploads the artifacts once, updates the track, then validates (dry run) or commits the
// edit with the retries the inputs allow. changesNotSentForReview is the flag the first commit is sent with. editLost
// reports that Google Play discarded the edit on the retry without sending for review, so the caller can start over.
func (p *Publisher) runEdit(service *androidpublisher.Service, configs Configs, changesNotSentForReview bool) (errorString string, editLost bool) {
	editsService := androidpublisher.NewEditsService(service)

	//
	// Create insert edit
	fmt.Println()
	p.logger.Infof("Create new edit")
	appEdit, err := editsService.Insert(configs.PackageName, &androidpublisher.AppEdit{}).Do()
	if err != nil {
		return fmt.Sprintf("Failed to perform edit insert call, error: %s", err), false
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
		return fmt.Sprintf("Failed to upload application(s): %v", err), false
	}
	p.logger.Donef("Applications uploaded")

	//
	// Update track
	var release *androidpublisher.TrackRelease
	if strings.TrimSpace(configs.Track) == "" {
		p.logger.Infof("Skipping track update")
	} else {
		fmt.Println()
		p.logger.Infof("Update track")
		release, err = p.createTrackRelease(configs, p.versionCodeMapToSlice(versionCodes))
		if err != nil {
			return fmt.Sprintf("Failed to update track, reason: %v", err), false
		}
		if err := p.updateTracks(configs, service, appEdit, release); err != nil {
			return fmt.Sprintf("Failed to update track, reason: %v", err), false
		}
		p.logger.Donef("Track updated")
	}

	if configs.DryRun {
		return p.validateEdit(editsService, configs, appEdit), false
	}
	return p.commitWithRetries(editsService, service, configs, appEdit, release, changesNotSentForReview)
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
// Google Play's cancel-and-resubmit default, so the review in progress is never cancelled by the Step. release is
// nil for a trackless upload.
func (p *Publisher) commitWithRetries(editsService *androidpublisher.EditsService, service *androidpublisher.Service, configs Configs, appEdit *androidpublisher.AppEdit, release *androidpublisher.TrackRelease, changesNotSentForReview bool) (errorString string, editLost bool) {
	retriedWithoutReview := false
	savedAsDraft := false

	for {
		fmt.Println()
		p.logger.Infof("Committing edit")
		if sendsErrorIfInReview(configs) {
			p.logger.Printf("A review in progress is not cancelled: the commit fails if Google Play is still reviewing an earlier submission of the app.")
		}
		err := p.commitEdit(editsService, configs, appEdit.Id, changesNotSentForReview)
		if err == nil {
			p.logger.Donef("Edit committed")
			if savedAsDraft {
				p.logger.Warnf("The release was saved as a draft on the %s track because a review is in progress. Roll it out from the Google Play Console once the review is complete.", configs.Track)
			}
			return "", false
		}

		switch {
		case isChangesNotSentForReviewRequired(err) && !changesNotSentForReview:
			if !configs.RetryWithoutSendingToReview {
				return fmt.Sprintf("Google Play does not send this app's changes for review automatically, so the commit was refused and nothing was committed (Google Play: %s). "+
					"Set retry_without_sending_to_review to true to commit without sending for review, then send the changes for review from the Google Play Console.", googleMessage(err)), false
			}
			p.logger.Warnf("Google Play does not send this app's changes for review automatically (Google Play: %s). Committing without sending for review; send the changes for review from the Google Play Console once the Step finishes.", googleMessage(err))
			changesNotSentForReview = true
			retriedWithoutReview = true

		case isChangesAlreadyInReview(err) && configs.ChangesInReviewBehavior == changesInReviewUploadAsDraft && !savedAsDraft:
			if advice := draftFallbackUnavailableAdvice(configs); advice != "" {
				return reviewInProgressFailure(err, advice), false
			}
			p.logger.Warnf("Google Play is still reviewing an earlier submission of this app (Google Play: %s). Saving the release as a draft on the %s track instead, without sending it for review.", googleMessage(err), configs.Track)
			draft := *release
			draft.Status = releaseStatusDraft
			draft.UserFraction = 0
			fmt.Println()
			p.logger.Infof("Update track")
			if err := p.updateTracks(configs, service, appEdit, &draft); err != nil {
				return fmt.Sprintf("Failed to update track, reason: %v", err), false
			}
			p.logger.Donef("Track updated")
			savedAsDraft = true

		case isChangesAlreadyInReview(err) && savedAsDraft:
			return fmt.Sprintf("Google Play refused to save the release as a draft while an earlier submission is in review, so nothing was committed and that review was left untouched (Google Play: %s). %s", googleMessage(err), waitOrCancelAdvice), false

		case isChangesAlreadyInReview(err):
			return reviewInProgressFailure(err, waitOrCancelAdvice+" To replace the review with these changes instead, set changes_in_review_behavior to cancel_and_resubmit."), false

		case retriedWithoutReview && isEditLost(err):
			return "", true

		default:
			return fmt.Sprintf("Failed to commit edit: %s", err), false
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

// sendsErrorIfInReview reports whether the commit is sent with changesInReviewBehavior=ERROR_IF_IN_REVIEW, which both
// fail and upload_as_draft need: neither may let Google Play cancel a review in progress.
func sendsErrorIfInReview(configs Configs) bool {
	return configs.ChangesInReviewBehavior == changesInReviewFail || configs.ChangesInReviewBehavior == changesInReviewUploadAsDraft
}

// commitEdit commits the edit. changesNotSentForReview and the changes_in_review_behavior input map to the commit
// call's query parameters; cancel_and_resubmit is Google Play's default, so it is not sent.
func (p *Publisher) commitEdit(editsService *androidpublisher.EditsService, configs Configs, editID string, changesNotSentForReview bool) error {
	call := editsService.Commit(configs.PackageName, editID)
	call.ChangesNotSentForReview(changesNotSentForReview)
	if sendsErrorIfInReview(configs) {
		call.ChangesInReviewBehavior("ERROR_IF_IN_REVIEW")
	}
	_, err := call.Do()
	return err
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

// isChangesNotSentForReviewRequired reports whether Google Play refused the commit because the app is in the state
// where changes are not sent for review automatically (after a rejection) and the flag has to be set.
func isChangesNotSentForReviewRequired(err error) bool {
	return err != nil && strings.Contains(err.Error(), changesNotSentForReviewMessage)
}

// isChangesAlreadyInReview reports whether Google Play refused the commit because a review is in progress: the
// reason is in the error's ErrorInfo details, or in the legacy errors list.
func isChangesAlreadyInReview(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, detail := range apiErr.Details {
		if info, ok := detail.(map[string]interface{}); ok && info["reason"] == changesAlreadyInReviewReason {
			return true
		}
	}
	for _, item := range apiErr.Errors {
		if item.Reason == changesAlreadyInReviewReason {
			return true
		}
	}
	return false
}

// editLostPattern matches Google Play's refusals of an edit it no longer accepts: deleted, expired, or superseded by
// a change made outside the edit.
var editLostPattern = regexp.MustCompile(`(?i)create a new edit|edit has been deleted|edit has expired|edit not found`)

// isEditLost reports whether the commit failed because the edit itself is gone rather than because of its content.
func isEditLost(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
		return true
	}
	return err != nil && editLostPattern.MatchString(googleMessage(err))
}
