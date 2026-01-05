package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bitrise-io/go-utils/fileutil"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
)

const (
	releaseStatusCompleted  = "completed"
	releaseStatusInProgress = "inProgress"
	releaseStatusDraft      = "draft"
	releaseStatusHalted     = "halted"
)

const bundleInstallationWarning = "Error 403: The installation of the app bundle may be too large " +
	"and trigger user warning on some devices, and this needs to be explicitly acknowledged in the request."

// uploadExpansionFiles uploads the expansion files for given applications, like .obb files.
func (p *Publisher) uploadExpansionFiles(service *androidpublisher.Service, expFileEntry string, packageName string, appEditID string, versionCode int64) error {
	cleanExpFileConfigEntry := strings.TrimSpace(expFileEntry)
	if !validateExpansionFileConfig(cleanExpFileConfigEntry) {
		return fmt.Errorf("invalid expansion file config: %s", expFileEntry)
	}

	expFilePth, expFileType, err := p.expFileInfo(cleanExpFileConfigEntry)
	if err != nil {
		return err
	}
	expansionFile, err := os.Open(expFilePth)
	if err != nil {
		return fmt.Errorf("failed to read expansion file (%v), error: %s", expansionFile, err)
	}
	p.logger.Debugf("Uploading expansion file %v with package name '%v', AppEditId '%v', version code '%v'", expansionFile, packageName, appEditID, versionCode)
	editsExpansionFilesService := androidpublisher.NewEditsExpansionfilesService(service)
	editsExpansionFilesCall := editsExpansionFilesService.Upload(packageName, appEditID, versionCode, expFileType)
	editsExpansionFilesCall.Media(expansionFile, googleapi.ContentType("application/octet-stream"))
	if _, err := editsExpansionFilesCall.Do(); err != nil {
		return fmt.Errorf("failed to upload expansion file, error: %s", err)
	}
	p.logger.Infof("Uploaded expansion file %v", expansionFile)
	return nil
}

// expFilePth gets the expansion file path from a given config entry
func (p *Publisher) expFileInfo(expFileConfigEntry string) (string, string, error) {
	// "main:/file/path/1.obb"
	expansionFilePathSplit := strings.Split(expFileConfigEntry, ":")
	if len(expansionFilePathSplit) < 2 {
		return "", "", fmt.Errorf("malformed expansion file path: %s", expFileConfigEntry)
	}

	// "main"
	expFileType := strings.TrimSpace(expansionFilePathSplit[0])
	p.logger.Debugf("Expansion file type is %s", expFileType)

	// "/file/path/1.obb"
	expFilePth := strings.TrimSpace(strings.Join(expansionFilePathSplit[1:], ""))
	p.logger.Debugf("Expansion file path is %s", expFilePth)
	return expFilePth, expFileType, nil
}

// validateExpansionFileConfig validates the given expansion file path. Returns false if it is neither a main or patch
// file. Example: "main:/file/path/1.obb".
func validateExpansionFileConfig(expFileEntry string) bool {
	cleanExpFileConfigEntry := strings.TrimSpace(expFileEntry)
	return strings.HasPrefix(cleanExpFileConfigEntry, "main:") || strings.HasPrefix(cleanExpFileConfigEntry, "patch:")
}

// uploadMappingFile uploads a given mapping file to a given app artifact (based on versionCode) to Google Play.
func (p *Publisher) uploadMappingFile(service *androidpublisher.Service, appEditID string, versionCode int64, packageName string, filePath string) error {
	p.logger.Debugf("Getting mapping file from %v", filePath)
	mappingFile, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to read mapping file (%s), error: %s", filePath, err)
	}
	p.logger.Debugf("Uploading mapping file %v with package name '%v', AppEditId '%v', version code '%v'", filePath, packageName, appEditID, versionCode)
	editsDeobfuscationFilesService := androidpublisher.NewEditsDeobfuscationfilesService(service)
	editsDeobfuscationFilesUploadCall := editsDeobfuscationFilesService.Upload(packageName, appEditID, versionCode, "proguard")
	editsDeobfuscationFilesUploadCall.Media(mappingFile, googleapi.ContentType("application/octet-stream"))

	if _, err = editsDeobfuscationFilesUploadCall.Do(); err != nil {
		return fmt.Errorf("failed to upload mapping file, error: %s", err)
	}

	p.logger.Printf(" uploaded mapping file for apk version: %d", versionCode)
	return nil
}

// uploadNativeSymbols uploads a given native debug symbols file to a given app artifact (based on versionCode) to Google Play.
func uploadNativeSymbols(service *androidpublisher.Service, appEditID string, versionCode int64, packageName string, filePath string) error {
	log.Debugf("Getting native symbols file from %v", filePath)
	nativeSymbolsFile, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to read native symbols file (%s), error: %s", filePath, err)
	}
	log.Debugf("Uploading native symbols file %v with package name '%v', AppEditId '%v', version code '%v'", filePath, packageName, appEditID, versionCode)
	editsDeobfuscationFilesService := androidpublisher.NewEditsDeobfuscationfilesService(service)
	editsDeobfuscationFilesUploadCall := editsDeobfuscationFilesService.Upload(packageName, appEditID, versionCode, "nativeCode")
	editsDeobfuscationFilesUploadCall.Media(nativeSymbolsFile, googleapi.ContentType("application/octet-stream"))

	if _, err = editsDeobfuscationFilesUploadCall.Do(); err != nil {
		return fmt.Errorf("failed to upload native symbols file, error: %s", err)
	}

	log.Printf(" uploaded native symbols file for apk version: %d", versionCode)
	return nil
}

// uploadAppBundle uploads aab files to Google Play. Returns the uploaded bundle itself or an error.
func (p *Publisher) uploadAppBundle(service *androidpublisher.Service, packageName string, appEditID string, appFile *os.File, ackBundleInstallationWarning bool) (*androidpublisher.Bundle, error) {
	p.logger.Debugf("Uploading file %v with package name '%v', AppEditId '%v", appFile, packageName, appEditID)
	editsBundlesService := androidpublisher.NewEditsBundlesService(service)

	editsBundlesUploadCall := editsBundlesService.Upload(packageName, appEditID)
	editsBundlesUploadCall.Media(appFile, googleapi.ContentType("application/octet-stream"))
	editsBundlesUploadCall.AckBundleInstallationWarning(ackBundleInstallationWarning)

	bundle, err := editsBundlesUploadCall.Do()
	if err != nil {
		msg := fmt.Sprintf("failed to upload app bundle, error: %s", err)
		if strings.Contains(err.Error(), bundleInstallationWarning) {
			msg = fmt.Sprintf("%s\nTo acknowledge this warning, set the Acknowledge Bundle Installation Warning (ack_bundle_installation_warning) input to true.", msg)
		}
		return nil, errors.New(msg)
	}
	p.logger.Infof("Uploaded app bundle version: %d", bundle.VersionCode)
	return bundle, nil
}

// uploadAppApk uploads an apk file to Google Play. Returns the apk itself or an error.
func (p *Publisher) uploadAppApk(service *androidpublisher.Service, packageName string, appEditID string, appFile *os.File) (*androidpublisher.Apk, error) {
	p.logger.Debugf("Uploading file %v with package name '%v', AppEditId '%v", appFile, packageName, appEditID)
	editsApksService := androidpublisher.NewEditsApksService(service)

	editsApksUploadCall := editsApksService.Upload(packageName, appEditID)
	editsApksUploadCall.Media(appFile, googleapi.ContentType("application/vnd.android.package-archive"))

	apk, err := editsApksUploadCall.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to upload apk, error: %s", err)
	}
	p.logger.Infof("Uploaded apk version: %d", apk.VersionCode)
	return apk, nil
}

// updates the listing info of a given release.
func (p *Publisher) updateListing(whatsNewsDir string, release *androidpublisher.TrackRelease) error {
	p.logger.Debugf("Checking if updating listing is required, whats new dir is '%v'", whatsNewsDir)
	if whatsNewsDir != "" {
		fmt.Println()
		p.logger.Infof("Update listing started")

		recentChangesMap, err := p.readLocalisedRecentChanges(whatsNewsDir)
		if err != nil {
			return fmt.Errorf("failed to read whatsnews, error: %s", err)
		}

		var releaseNotes []*androidpublisher.LocalizedText
		for language, recentChanges := range recentChangesMap {
			releaseNotes = append(releaseNotes, &androidpublisher.LocalizedText{
				Language: language,
				Text:     recentChanges,
			})
		}
		release.ReleaseNotes = releaseNotes
		p.logger.Infof("Update listing finished")
	}
	return nil
}

// readLocalisedRecentChanges reads the recent changes from the given path and returns them as a map.
func (p *Publisher) readLocalisedRecentChanges(recentChangesDir string) (map[string]string, error) {
	recentChangesMap := map[string]string{}

	pattern := filepath.Join(recentChangesDir, "whatsnew-*")
	recentChangesPaths, err := filepath.Glob(pattern)
	if err != nil {
		return map[string]string{}, err
	}

	// The language code (a BCP-47 language tag) of the localized listing to read or modify
	// https://tools.ietf.org/html/bcp47#section-2.1
	pattern = `whatsnew-(?P<locale>([0-9a-zA-Z].*(-|$))+)`
	re := regexp.MustCompile(pattern)

	for _, recentChangesPath := range recentChangesPaths {
		matches := re.FindStringSubmatch(recentChangesPath)
		if len(matches) >= 2 {
			language := matches[1]
			content, err := fileutil.ReadStringFromFile(recentChangesPath)
			if err != nil {
				return map[string]string{}, err
			}

			recentChangesMap[language] = content
		}
	}
	if len(recentChangesMap) > 0 {
		p.logger.Debugf("Found the following recent changes:")
		for language, recentChanges := range recentChangesMap {
			p.logger.Debugf("%v\n", language)
			p.logger.Debugf("Content: %v", recentChanges)
		}
	} else {
		p.logger.Debugf("No recent changes found")
	}

	return recentChangesMap, nil
}

// createTrackRelease returns a release object with the given version codes and adds the listing information.
func (p *Publisher) createTrackRelease(config Configs, versionCodes googleapi.Int64s) (*androidpublisher.TrackRelease, error) {
	newRelease := &androidpublisher.TrackRelease{
		VersionCodes:        versionCodes,
		Status:              config.Status,
		InAppUpdatePriority: int64(config.UpdatePriority),
	}
	p.logger.Infof("Release version codes are: %v", newRelease.VersionCodes)

	if newRelease.Status == "" {
		newRelease.Status = p.releaseStatusFromConfig(config.UserFraction)
	}

	if shouldApplyUserFraction(newRelease.Status) {
		newRelease.UserFraction = config.UserFraction
	}

	if config.ReleaseName != "" {
		newRelease.Name = config.ReleaseName
	}

	if err := p.updateListing(config.WhatsnewsDir, newRelease); err != nil {
		return nil, fmt.Errorf("failed to update listing, reason: %v", err)
	}

	return newRelease, nil
}

// releaseStatusFromConfig gets the release status from the config value of user fraction.
func (p *Publisher) releaseStatusFromConfig(userFraction float64) string {
	if userFraction != 0 {
		p.logger.Infof("Release is a staged rollout, %v of users will receive it.", userFraction)
		return releaseStatusInProgress
	}
	return releaseStatusCompleted
}

func shouldApplyUserFraction(status string) bool {
	return status == releaseStatusInProgress || status == releaseStatusHalted
}
