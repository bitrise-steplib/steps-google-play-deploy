package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
)

const (
	releaseStatusCompleted  = "completed"
	releaseStatusInProgress = "inProgress"
)

// uploadExpansionFiles uploads the expansion files for given applications, like .obb files.
func uploadExpansionFiles(service *androidpublisher.Service, expFileEntry string, packageName string, appEditID string, versionCode int64) error {
	cleanExpFileConfigEntry := strings.TrimSpace(expFileEntry)
	if !validateExpansionFileConfig(cleanExpFileConfigEntry) {
		return fmt.Errorf("invalid expansion file config: %s", expFileEntry)
	}

	expFilePth, expFileType, err := expFileInfo(cleanExpFileConfigEntry)
	if err != nil {
		return err
	}
	expansionFile, err := os.Open(expFilePth)
	if err != nil {
		return fmt.Errorf("failed to read expansion file (%v), error: %s", expansionFile, err)
	}
	log.Debugf("Uploading expansion file %v with package name '%v', AppEditId '%v', version code '%v'", expansionFile, packageName, appEditID, versionCode)
	editsExpansionFilesService := androidpublisher.NewEditsExpansionfilesService(service)
	editsExpansionFilesCall := editsExpansionFilesService.Upload(packageName, appEditID, versionCode, expFileType)
	editsExpansionFilesCall.Media(expansionFile, googleapi.ContentType("application/octet-stream"))
	if _, err := editsExpansionFilesCall.Do(); err != nil {
		return fmt.Errorf("failed to upload expansion file, error: %s", err)
	}
	log.Infof("Uploaded expansion file %v", expansionFile)
	return nil
}

// expFilePth gets the expansion file path from a given config entry
func expFileInfo(expFileConfigEntry string) (string, string, error) {
	// "main:/file/path/1.obb"
	expansionFilePathSplit := strings.Split(expFileConfigEntry, ":")
	if len(expansionFilePathSplit) < 2 {
		return "", "", fmt.Errorf("malformed expansion file path: %s", expFileConfigEntry)
	}

	// "main"
	expFileType := strings.TrimSpace(expansionFilePathSplit[0])
	log.Debugf("Expansion file type is %s", expFileType)

	// "/file/path/1.obb"
	expFilePth := strings.TrimSpace(strings.Join(expansionFilePathSplit[1:], ""))
	log.Debugf("Expansion file path is %s", expFilePth)
	return expFilePth, expFileType, nil
}

// validateExpansionFileConfig validates the given expansion file path. Returns false if it is neither a main or patch
// file. Example: "main:/file/path/1.obb".
func validateExpansionFileConfig(expFileEntry string) bool {
	cleanExpFileConfigEntry := strings.TrimSpace(expFileEntry)
	return strings.HasPrefix(cleanExpFileConfigEntry, "main:") || strings.HasPrefix(cleanExpFileConfigEntry, "patch:")
}

// uploadMappingFile uploads the mapping files (that are used for deobfuscation) to Google Play.
func uploadMappingFile(service *androidpublisher.Service, configs Configs, appEditID string, versionCode int64) error {
	log.Debugf("Getting mapping file from %v", configs.MappingFile)
	mappingFile, err := os.Open(configs.MappingFile)
	if err != nil {
		return fmt.Errorf("failed to read mapping file (%s), error: %s", configs.MappingFile, err)
	}
	log.Debugf("Uploading mapping file %v with package name '%v', AppEditId '%v', version code '%v'", configs.MappingFile, configs.PackageName, appEditID, versionCode)
	editsDeobfuscationFilesService := androidpublisher.NewEditsDeobfuscationfilesService(service)
	editsDeobfuscationFilesUploadCall := editsDeobfuscationFilesService.Upload(configs.PackageName, appEditID, versionCode, "proguard")
	editsDeobfuscationFilesUploadCall.Media(mappingFile, googleapi.ContentType("application/octet-stream"))

	if _, err = editsDeobfuscationFilesUploadCall.Do(); err != nil {
		return fmt.Errorf("failed to upload mapping file, error: %s", err)
	}

	log.Printf(" uploaded mapping file for apk version: %d", versionCode)
	return nil
}

// uploadAppBundle uploads aab files to Google Play. Returns the uploaded bundle itself or an error.
func uploadAppBundle(service *androidpublisher.Service, packageName string, appEditID string, appFile *os.File) (*androidpublisher.Bundle, error) {
	log.Debugf("Uploading file %v with package name '%v', AppEditId '%v", appFile, packageName, appEditID)
	editsBundlesService := androidpublisher.NewEditsBundlesService(service)

	editsBundlesUploadCall := editsBundlesService.Upload(packageName, appEditID)
	editsBundlesUploadCall.Media(appFile, googleapi.ContentType("application/octet-stream"))

	bundle, err := editsBundlesUploadCall.Do()
	if err != nil {
		return &androidpublisher.Bundle{}, fmt.Errorf("failed to upload app bundle, error: %s", err)
	}
	log.Infof("Uploaded app bundle version: %d", bundle.VersionCode)
	return bundle, nil
}

// uploadAppApk uploads an apk file to Google Play. Returns the apk itself or an error.
func uploadAppApk(service *androidpublisher.Service, packageName string, appEditID string, appFile *os.File) (*androidpublisher.Apk, error) {
	log.Debugf("Uploading file %v with package name '%v', AppEditId '%v", appFile, packageName, appEditID)
	editsApksService := androidpublisher.NewEditsApksService(service)

	editsApksUploadCall := editsApksService.Upload(packageName, appEditID)
	editsApksUploadCall.Media(appFile, googleapi.ContentType("application/vnd.android.package-archive"))

	apk, err := editsApksUploadCall.Do()
	if err != nil {
		return &androidpublisher.Apk{}, fmt.Errorf("failed to upload apk, error: %s", err)
	}
	log.Infof("Uploaded apk version: %d", apk.VersionCode)
	return apk, nil
}

// updates the listing info of a given release.
func updateListing(whatsNewsDir string, release *androidpublisher.TrackRelease) error {
	log.Debugf("Checking if updating listing is required, whats new dir is '%v'", whatsNewsDir)
	if whatsNewsDir != "" {
		fmt.Println()
		log.Infof("Update listing started")

		recentChangesMap, err := readLocalisedRecentChanges(whatsNewsDir)
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
		log.Infof("Update listing finished")
	}
	return nil
}

// readLocalisedRecentChanges reads the recent changes from the given path and returns them as a map.
func readLocalisedRecentChanges(recentChangesDir string) (map[string]string, error) {
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
		log.Debugf("Found the following recent changes:")
		for language, recentChanges := range recentChangesMap {
			log.Debugf("%v\n", language)
			log.Debugf("Content: %v", recentChanges)
		}
	} else {
		log.Debugf("No recent changes found")
	}

	return recentChangesMap, nil
}

// createTrackRelease returns a release object with the given version codes and adds the listing information.
func createTrackRelease(whatsNewsDir string, versionCodes googleapi.Int64s, userFraction float64, updatePriority int, releaseName string) (*androidpublisher.TrackRelease, error) {
	status := releaseStatusFromConfig(userFraction)

	newRelease := &androidpublisher.TrackRelease{
		VersionCodes:        versionCodes,
		Status:              status,
		InAppUpdatePriority: int64(updatePriority),
	}
	log.Infof("Release version codes are: %v", newRelease.VersionCodes)
	if userFraction != 0 {
		newRelease.UserFraction = userFraction
	}

	if releaseName != "" {
		newRelease.Name = releaseName
	}

	if err := updateListing(whatsNewsDir, newRelease); err != nil {
		return nil, fmt.Errorf("failed to update listing, reason: %v", err)
	}

	return newRelease, nil
}

// releaseStatusFromConfig gets the release status from the config value of user fraction.
func releaseStatusFromConfig(userFraction float64) string {
	if userFraction != 0 {
		log.Infof("Release is a staged rollout, %v of users will receive it.", userFraction)
		return releaseStatusInProgress
	}
	return releaseStatusCompleted
}
