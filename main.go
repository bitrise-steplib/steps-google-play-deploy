package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"
)

const (
	alphaTrackName      = "alpha"
	betaTrackName       = "beta"
	productionTrackName = "production"
	rolloutTrackName    = "rollout"

	releaseStatusCompleted  = "completed"
	releaseStatusDraft      = "draft"
	releaseStatusHalted     = "halted"
	releaseStatusInProgress = "inProgress"
)

func downloadFile(downloadURL, targetPath string) error {
	outFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed to create (%s), error: %s", targetPath, err)
	}
	defer func() {
		if err := outFile.Close(); err != nil {
			log.Warnf("Failed to close (%s)", targetPath)
		}
	}()

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("failed to close (%s) body", downloadURL)
		}
	}()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}

	return nil
}

func jwtConfigFromJSONKeyFile(pth string) (*jwt.Config, error) {
	jsonKeyBytes, err := fileutil.ReadBytesFromFile(pth)
	if err != nil {
		return nil, err
	}

	config, err := google.JWTConfigFromJSON(jsonKeyBytes, androidpublisher.AndroidpublisherScope)
	if err != nil {
		return nil, err
	}

	return config, nil
}

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

	return recentChangesMap, nil
}

func parseURI(keyPath string) (string, bool, error) {
	jsonURL, err := url.Parse(keyPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse url (%s), error: %s", keyPath, err)
	}

	return strings.TrimPrefix(keyPath, "file://"), jsonURL.Scheme == "http" || jsonURL.Scheme == "https", nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

func createHTTPClient(jsonKeyPth string) (*http.Client, error) {
	jwtConfig := new(jwt.Config)
	jsonKeyPth, isRemote, err := parseURI(string(jsonKeyPth))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare key path (%s), error: %s", jsonKeyPth, err)
	}

	if isRemote {
		tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
		if err != nil {
			return nil, fmt.Errorf("failed to create tmp dir, error: %s", err)
		}

		jsonKeySource := jsonKeyPth
		jsonKeyPth = filepath.Join(tmpDir, "key.json")
		if err := downloadFile(jsonKeySource, jsonKeyPth); err != nil {
			return nil, fmt.Errorf("failed to download json key file, error: %s", err)
		}
	}

	authConfig, err := jwtConfigFromJSONKeyFile(jsonKeyPth)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth config from json key file, error: %s", err)
	}
	jwtConfig = authConfig

	return jwtConfig.Client(context.TODO()), nil
}

func uploadApks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) ([]int64, error) {
	var versionCodes []int64
	appPaths, _ := configs.appPaths()

	// "main:/file/path/1.obb|patch:/file/path/2.obb"
	expansionFileUpload := strings.TrimSpace(configs.ExpansionfilePath) != ""
	expansionFilePaths := strings.Split(configs.ExpansionfilePath, "|")

	if expansionFileUpload && (len(appPaths) != len(expansionFilePaths)) {
		return []int64{}, fmt.Errorf("mismatching number of APKs(%d) and Expansionfiles(%d)", len(appPaths), len(expansionFilePaths))
	}

	for i, appPath := range appPaths {
		log.Printf("Uploading %v", appPath)
		versionCode := int64(0)
		appFile, err := os.Open(appPath)
		if err != nil {
			return []int64{}, fmt.Errorf("failed to open app (%s), error: %s", appPath, err)
		}

		if strings.ToLower(filepath.Ext(appPath)) == ".aab" {
			editsBundlesService := androidpublisher.NewEditsBundlesService(service)

			editsBundlesUploadCall := editsBundlesService.Upload(configs.PackageName, appEdit.Id)
			editsBundlesUploadCall.Media(appFile, googleapi.ContentType("application/octet-stream"))

			bundle, err := editsBundlesUploadCall.Do()
			if err != nil {
				return []int64{}, fmt.Errorf("failed to upload app bundle, error: %s", err)
			}
			log.Printf(" uploaded app bundle version: %d", bundle.VersionCode)
			versionCodes = append(versionCodes, bundle.VersionCode)
			versionCode = bundle.VersionCode
		} else {
			editsApksService := androidpublisher.NewEditsApksService(service)

			editsApksUploadCall := editsApksService.Upload(configs.PackageName, appEdit.Id)
			editsApksUploadCall.Media(appFile, googleapi.ContentType("application/vnd.android.package-archive"))

			apk, err := editsApksUploadCall.Do()
			if err != nil {
				return []int64{}, fmt.Errorf("failed to upload apk, error: %s", err)
			}

			log.Printf(" uploaded apk version: %d", apk.VersionCode)
			versionCodes = append(versionCodes, apk.VersionCode)
			versionCode = apk.VersionCode

			if expansionFileUpload {
				// "main:/file/path/1.obb"
				cleanExpfilePath := strings.TrimSpace(expansionFilePaths[i])
				if !strings.HasPrefix(cleanExpfilePath, "main:") && !strings.HasPrefix(cleanExpfilePath, "patch:") {
					return []int64{}, fmt.Errorf("invalid expansion file config: %s", expansionFilePaths[i])
				}

				// [0]: "main" [1]:"/file/path/1.obb"
				expansionfilePathSplit := strings.Split(cleanExpfilePath, ":")

				// "main"
				expfileType := strings.TrimSpace(expansionfilePathSplit[0])

				// "/file/path/1.obb"
				expfilePth := strings.TrimSpace(strings.Join(expansionfilePathSplit[1:], ""))

				expansionFile, err := os.Open(expfilePth)
				if err != nil {
					return []int64{}, fmt.Errorf("failed to read expansion file (%v), error: %s", expansionFile, err)
				}
				editsExpansionFilesService := androidpublisher.NewEditsExpansionfilesService(service)
				editsExpansionFilesCall := editsExpansionFilesService.Upload(configs.PackageName, appEdit.Id, versionCode, expfileType)
				editsExpansionFilesCall.Media(expansionFile, googleapi.ContentType("application/vnd.android.package-archive"))
				if _, err := editsExpansionFilesCall.Do(); err != nil {
					return []int64{}, fmt.Errorf("failed to upload expansion file, error: %s", err)
				}
			}
		}

		// Upload mapping.txt
		if configs.MappingFile != "" && versionCode != 0 {
			mappingFile, err := os.Open(configs.MappingFile)
			if err != nil {
				return []int64{}, fmt.Errorf("failed to read mapping file (%s), error: %s", configs.MappingFile, err)
			}
			editsDeobfuscationFilesService := androidpublisher.NewEditsDeobfuscationfilesService(service)
			editsDeobfuscationFilesUploadCall := editsDeobfuscationFilesService.Upload(configs.PackageName, appEdit.Id, versionCode, "proguard")
			editsDeobfuscationFilesUploadCall.Media(mappingFile, googleapi.ContentType("application/octet-stream"))

			if _, err = editsDeobfuscationFilesUploadCall.Do(); err != nil {
				return []int64{}, fmt.Errorf("failed to upload mapping file, error: %s", err)
			}

			log.Printf(" uploaded mapping file for apk version: %d", versionCode)
			if i < len(appPaths)-1 {
				fmt.Println()
			}
		}
	}
	log.Printf("Done uploading of %v apps", len(appPaths))
	log.Printf("New version codes to upload: %v", versionCodes)
	return versionCodes, nil
}

func updateTrack(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, versionCodes []int64) error {
	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newTrack, err := getTrack(configs, service, appEdit, configs.Track)
	if err != nil {
		return err
	}

	newRelease, err := getNewRelease(configs, versionCodes)
	if err != nil {
		return err
	}
	newTrack.Releases = append(newTrack.Releases, &newRelease)
	printTrack(newTrack, "New track to upload:")

	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, newTrack)
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		return fmt.Errorf("update call failed, error: %s", err)
	}

	log.Printf(" updated track: %s", track.Track)
	log.Printf(" assigned apk versions: %v", newRelease.VersionCodes)
	return nil
}

func printTrack(track *androidpublisher.Track, prefix string) {
	log.Printf("%s\n", prefix)
	for _, release := range track.Releases {
		printRelease(*release)
	}
}

func printRelease(release androidpublisher.TrackRelease) {
	log.Printf("Release '%s' has versionCodes: %v", release.Name, release.VersionCodes)
}

func getTrack(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit, currentTrack string) (*androidpublisher.Track, error) {
	listResponse, err := listTracks(configs, service, appEdit)
	if err != nil {
		return &androidpublisher.Track{}, fmt.Errorf("failed to list tracks, error: %s", err)
	}
	for _, track := range listResponse.Tracks {
		if currentTrack == track.Track {
			return track, nil
		}
	}

	return &androidpublisher.Track{
		Releases:        []*androidpublisher.TrackRelease{},
		Track:           currentTrack,
		ServerResponse:  googleapi.ServerResponse{},
		ForceSendFields: []string{},
		NullFields:      []string{},
	}, nil
}

func listTracks(configs Configs, service *androidpublisher.Service, appEdit *androidpublisher.AppEdit) (*androidpublisher.TracksListResponse, error) {
	log.Printf("Listing tracks")
	tracksService := androidpublisher.NewEditsTracksService(service)
	tracksListCall := tracksService.List(configs.PackageName, appEdit.Id)
	listResponse, err := tracksListCall.Do()
	if err != nil {
		return &androidpublisher.TracksListResponse{}, fmt.Errorf("failed to list tracks, error: %s", err)
	}
	for _, track := range listResponse.Tracks {
		printTrack(track, "Found track:")
	}
	return listResponse, nil
}

func getNewRelease(configs Configs, versionCodes googleapi.Int64s) (androidpublisher.TrackRelease, error) {
	newRelease := androidpublisher.TrackRelease{
		VersionCodes: versionCodes,
	}

	if configs.UserFraction != 0 {
		log.Infof("Release is a staged rollout, %v of users will receive it.", configs.UserFraction)
		newRelease.UserFraction = configs.UserFraction
		newRelease.Status = releaseStatusInProgress
	} else {
		newRelease.Status = releaseStatusCompleted
	}
	if err := updateListing(configs, &newRelease); err != nil {
		return newRelease, fmt.Errorf("failed to update listing, reason: %v", err)
	}
	return newRelease, nil
}

func updateListing(configs Configs, release *androidpublisher.TrackRelease) error {
	if configs.WhatsnewsDir != "" {
		fmt.Println()
		log.Infof("Update listing")

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
	}
	return nil
}

func main() {
	var configs Configs
	if err := stepconf.Parse(&configs); err != nil {
		failf("Couldn't create config: %s\n", err)
	}
	stepconf.Print(configs)
	if err := configs.validate(); err != nil {
		failf(err.Error())
	}

	//
	// Create client
	fmt.Println()
	log.Infof("Authenticating")
	client, err := createHTTPClient(string(configs.JSONKeyPath))
	if err != nil {
		failf("Failed to create http client: %v", err)
	}
	service, err := androidpublisher.New(client)
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
	// ---

	//
	// Upload APKs
	fmt.Println()
	log.Infof("Upload apks or app bundle")
	versionCodes, err := uploadApks(configs, service, appEdit)
	if err != nil {
		failf("Failed to upload APKs: %v", err)
	}

	// Update track
	fmt.Println()
	log.Infof("Update track")
	if err := updateTrack(configs, service, appEdit, versionCodes); err != nil {
		failf("Failed to update track, reason: %v", err)
	}

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
