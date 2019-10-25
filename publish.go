package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// expansionFiles gets the expansion files from the received configuration. Returns true and the entries (type and
// path) of them when any found, false or error otherwise.
func expansionFiles(appPaths []string, expansionFilePathConfig string) (bool, []string, error) {
	// "main:/file/path/1.obb|patch:/file/path/2.obb"
	expansionFileUpload := strings.TrimSpace(expansionFilePathConfig) != ""
	expansionFileEntries := strings.Split(expansionFilePathConfig, "|")

	if expansionFileUpload && (len(appPaths) != len(expansionFileEntries)) {
		return false, []string{}, fmt.Errorf("mismatching number of APKs(%d) and Expansionfiles(%d)", len(appPaths), len(expansionFileEntries))
	}
	if expansionFileUpload {
		log.Infof("Found %v expansion file(s) to upload.", len(expansionFileEntries))
		for i, expansionFile := range expansionFileEntries {
			log.Debugf("%v - %v", i+1, expansionFile)
		}
	}
	return expansionFileUpload, expansionFileEntries, nil
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
func updateListing(configs Configs, release *androidpublisher.TrackRelease) error {
	log.Debugf("Checking if updating listing is required, whats new dir is '%v'", configs.WhatsnewsDir)
	if configs.WhatsnewsDir != "" {
		fmt.Println()
		log.Infof("Update listing started")

		recentChangesMap, err := readLocalisedRecentChanges(configs.WhatsnewsDir)
		if err != nil {
			return fmt.Errorf("failed to read whatsnews, error: %s", err)
		}

		var releaseNotes []*androidpublisher.LocalizedText
		for language, recentChanges := range recentChangesMap {
			releaseNotes = append(releaseNotes, &androidpublisher.LocalizedText{
				Language:        language,
				Text:            recentChanges,
				ForceSendFields: []string{},
				NullFields:      []string{},
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

	pattern := filepath.Join(recentChangesDir, "whatsnew-*-*")
	recentChangesPaths, err := filepath.Glob(pattern)
	if err != nil {
		return map[string]string{}, err
	}

	pattern = `whatsnew-(?P<local>.*-.*)`
	re := regexp.MustCompile(pattern)

	for _, recentChangesPath := range recentChangesPaths {
		matches := re.FindStringSubmatch(recentChangesPath)
		if len(matches) == 2 {
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

// getRelease gets a release from a track based on it's status. Note we would get error if we would like to have
// multiple instances of a release with the same status. Example: "error: googleapi: Error 400: Too many completed
// releases specified., releasesTooManyCompletedReleases".
func getRelease(userFraction float64, releases *[]*androidpublisher.TrackRelease) *androidpublisher.TrackRelease {
	status := releaseStatusFromConfig(userFraction)
	for _, release := range *releases {
		if status == release.Status {
			return release
		}
	}
	newRelease := androidpublisher.TrackRelease{
		Status: status,
	}
	if userFraction != 0 {
		newRelease.UserFraction = userFraction
	}
	*releases = append(*releases, &newRelease)
	return &newRelease
}

// updateRelease creates and returns a new release object with the given version codes.
func updateRelease(configs Configs, versionCodes googleapi.Int64s, release *androidpublisher.TrackRelease) error {
	if configs.UntrackBlockingVersions {
		removeBlockingVersionFromRelease(release, versionCodes)
	} else {
		release.VersionCodes = append(release.VersionCodes, versionCodes...)
	}
	log.Infof("Release version codes are: %v", release.VersionCodes)

	log.Printf(" assigned app versions: %v", release.VersionCodes)
	if err := updateListing(configs, release); err != nil {
		return fmt.Errorf("failed to update listing, reason: %v", err)
	}
	return nil
}

// removeBlockingVersionFromRelease removes blocking version from a given version, which would shadow the given version.
func removeBlockingVersionFromRelease(release *androidpublisher.TrackRelease, newVersionCodes googleapi.Int64s) {
	log.Printf("Checking app versions on release: %s", release.Name)
	log.Infof("Current version codes: %v", release.VersionCodes)
	log.Infof("New version codes: '%v'", newVersionCodes)

	var cleanTrack bool
	if len(release.VersionCodes) != len(newVersionCodes) {
		log.Warnf("Mismatching app count, removing (%v) versions from release: %s", release.VersionCodes, release.Name)
		cleanTrack = true
	} else {
		log.Debugf("The number of App version codes (current and new) are equal")
		release.VersionCodes = sortAndFilterVersionCodes(release.VersionCodes, newVersionCodes)
	}

	if cleanTrack {
		log.Infof("Clearing version codes for release %v, the new versions will be %v", release.Name, newVersionCodes)
		release.VersionCodes = newVersionCodes
	}
}

// sortAndFilterVersionCodes sorts and filters two set of version codes, returns the higher for each.
func sortAndFilterVersionCodes(currentVersionCodes googleapi.Int64s, newVersionCodes googleapi.Int64s) googleapi.Int64s {
	sort.Slice(currentVersionCodes, func(a, b int) bool { return currentVersionCodes[a] < currentVersionCodes[b] })
	sort.Slice(newVersionCodes, func(a, b int) bool { return newVersionCodes[a] < newVersionCodes[b] })

	var filteredVersionCodes googleapi.Int64s
	for i := 0; i < len(newVersionCodes); i++ {
		log.Debugf("Searching for shadowing versions, comparing (%v) and (%v)", currentVersionCodes[i], newVersionCodes[i])
		if currentVersionCodes[i] < newVersionCodes[i] {
			log.Infof("Shadowing app found, removing current (%v) version, adding new (%v)", currentVersionCodes[i], newVersionCodes[i])
			filteredVersionCodes = append(filteredVersionCodes, newVersionCodes[i])
		} else {
			log.Infof("Currently released app with (%v) version is higher than new (%v), app with new version code ignored", currentVersionCodes[i], newVersionCodes[i])
			filteredVersionCodes = append(filteredVersionCodes, currentVersionCodes[i])
		}
	}
	return filteredVersionCodes
}

// releaseStatusFromConfig gets the release status from the config value of user fraction.
func releaseStatusFromConfig(userFraction float64) string {
	if userFraction != 0 {
		log.Infof("Release is a staged rollout, %v of users will receive it.", userFraction)
		return releaseStatusInProgress
	}
	return releaseStatusCompleted
}

// getTrack gets the given track from the list of tracks of a given app.
func getTrack(configs Configs, allTracks []*androidpublisher.Track) *androidpublisher.Track {
	currentTrack := configs.Track
	for _, track := range allTracks {
		if currentTrack == track.Track {
			log.Debugf("Current track found, name '%s'", currentTrack)
			return track
		}
	}

	return &androidpublisher.Track{
		Releases:        []*androidpublisher.TrackRelease{},
		Track:           currentTrack,
		ServerResponse:  googleapi.ServerResponse{},
		ForceSendFields: []string{},
		NullFields:      []string{},
	}
}

// getAllTracks lists all tracks for a given app.
func getAllTracks(packageName string, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) ([]*androidpublisher.Track, error) {
	log.Infof("Listing tracks")
	tracksService := androidpublisher.NewEditsTracksService(service)
	tracksListCall := tracksService.List(packageName, appEdit.Id)
	listResponse, err := tracksListCall.Do()
	if err != nil {
		return []*androidpublisher.Track{}, fmt.Errorf("failed to list tracks, error: %s", err)
	}
	for _, track := range listResponse.Tracks {
		PrintTrack(track, "Found track:")
	}
	return listResponse.Tracks, nil
}
