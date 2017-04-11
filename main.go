package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"

	"google.golang.org/api/androidpublisher/v2"
	"google.golang.org/api/googleapi"

	"net/url"

	"github.com/bitrise-io/go-utils/command"
	"github.com/bitrise-io/go-utils/errorutil"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"
)

const (
	alphaTrackName      = "alpha"
	betaTrackName       = "beta"
	rolloutTrackName    = "rollout"
	productionTrackName = "production"
)

// ConfigsModel ...
type ConfigsModel struct {
	ServiceAccountEmail     string
	P12KeyPath              string
	JSONKeyPath             string
	PackageName             string
	ApkPath                 string
	Track                   string
	UserFraction            string
	WhatsnewsDir            string
	UntrackBlockingVersions string
}

func createConfigsModelFromEnvs() ConfigsModel {
	return ConfigsModel{
		ServiceAccountEmail:     os.Getenv("service_account_email"),
		P12KeyPath:              os.Getenv("key_file_path"),
		JSONKeyPath:             os.Getenv("service_account_json_key_path"),
		PackageName:             os.Getenv("package_name"),
		ApkPath:                 os.Getenv("apk_path"),
		Track:                   os.Getenv("track"),
		UserFraction:            os.Getenv("user_fraction"),
		WhatsnewsDir:            os.Getenv("whatsnews_dir"),
		UntrackBlockingVersions: os.Getenv("untrack_blocking_versions"),
	}
}

func secureInput(str string) string {
	if str == "" {
		return ""
	}

	secureStr := func(s string, show int) string {
		runeCount := utf8.RuneCountInString(s)
		if runeCount < 6 || show == 0 {
			return strings.Repeat("*", 3)
		}
		if show*4 > runeCount {
			show = 1
		}

		sec := fmt.Sprintf("%s%s%s", s[0:show], strings.Repeat("*", 3), s[len(s)-show:len(s)])
		return sec
	}

	prefix := ""
	cont := str
	sec := secureStr(cont, 0)

	if strings.HasPrefix(str, "file://") {
		prefix = "file://"
		cont = strings.TrimPrefix(str, prefix)
		sec = secureStr(cont, 3)
	} else if strings.HasPrefix(str, "http://www.") {
		prefix = "http://www."
		cont = strings.TrimPrefix(str, prefix)
		sec = secureStr(cont, 3)
	} else if strings.HasPrefix(str, "https://www.") {
		prefix = "https://www."
		cont = strings.TrimPrefix(str, prefix)
		sec = secureStr(cont, 3)
	} else if strings.HasPrefix(str, "http://") {
		prefix = "http://"
		cont = strings.TrimPrefix(str, prefix)
		sec = secureStr(cont, 3)
	} else if strings.HasPrefix(str, "https://") {
		prefix = "https://"
		cont = strings.TrimPrefix(str, prefix)
		sec = secureStr(cont, 3)
	}

	return prefix + sec
}

func (configs ConfigsModel) print() {
	log.Infof("Configs:")
	log.Printf("- JSONKeyPath: %s", secureInput(configs.JSONKeyPath))
	log.Printf("- PackageName: %s", configs.PackageName)
	log.Printf("- ApkPath: %s", configs.ApkPath)
	log.Printf("- Track: %s", configs.Track)
	log.Printf("- UserFraction: %s", configs.UserFraction)
	log.Printf("- WhatsnewsDir: %s", configs.WhatsnewsDir)
	log.Infof("Deprecated Configs:")
	log.Printf("- ServiceAccountEmail: %s", secureInput(configs.ServiceAccountEmail))
	log.Printf("- P12KeyPath: %s", secureInput(configs.P12KeyPath))
	log.Printf("- UntrackBlockingVersions: %s", configs.UntrackBlockingVersions)
}

func (configs ConfigsModel) validate() error {
	// required
	if configs.JSONKeyPath != "" {
		if strings.HasPrefix(configs.JSONKeyPath, "file://") {
			pth := strings.TrimPrefix(configs.JSONKeyPath, "file://")
			if exist, err := pathutil.IsPathExists(pth); err != nil {
				return fmt.Errorf("Failed to check if JSONKeyPath exist at: %s, error: %s", pth, err)
			} else if !exist {
				return fmt.Errorf("JSONKeyPath not exist at: %s", pth)
			}
		}

	} else if configs.P12KeyPath != "" {
		if strings.HasPrefix(configs.P12KeyPath, "file://") {
			pth := strings.TrimPrefix(configs.P12KeyPath, "file://")
			if exist, err := pathutil.IsPathExists(pth); err != nil {
				return fmt.Errorf("Failed to check if P12KeyPath exist at: %s, error: %s", pth, err)
			} else if !exist {
				return fmt.Errorf("P12KeyPath not exist at: %s", pth)
			}
		}
		if configs.ServiceAccountEmail == "" {
			return fmt.Errorf("No ServiceAccountEmail parameter specified")
		}
	} else {
		return errors.New("No JSONKeyPath nor P12KeyPath provided")
	}

	if configs.PackageName == "" {
		return errors.New("No PackageName parameter specified")
	}

	if configs.ApkPath == "" {
		return errors.New("No ApkPath parameter specified")
	}
	apkPaths := strings.Split(configs.ApkPath, "|")
	for _, apkPath := range apkPaths {
		if exist, err := pathutil.IsPathExists(apkPath); err != nil {
			return fmt.Errorf("Failed to check if APK exist at: %s, error: %s", apkPath, err)
		} else if !exist {
			return fmt.Errorf("APK not exist at: %s", apkPath)
		}
	}

	if configs.Track == "" {
		return errors.New("No Track parameter specified")
	}

	if configs.Track == rolloutTrackName {
		if configs.UserFraction == "" {
			return errors.New("No UserFraction parameter specified")
		}
	}

	if configs.WhatsnewsDir != "" {
		if exist, err := pathutil.IsPathExists(configs.WhatsnewsDir); err != nil {
			return fmt.Errorf("Failed to check if WhatsnewsDir exist at: %s, error: %s", configs.WhatsnewsDir, err)
		} else if !exist {
			return fmt.Errorf("WhatsnewsDir not exist at: %s", configs.WhatsnewsDir)
		}
	}

	return nil
}

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

func jwtConfigFromP12KeyFile(pth, email string) (*jwt.Config, error) {
	cmd := command.New("openssl", "pkcs12", "-in", pth, "-passin", "pass:notasecret", "-nodes")

	var outBuffer bytes.Buffer
	outWriter := bufio.NewWriter(&outBuffer)
	cmd.SetStdout(outWriter)

	var errBuffer bytes.Buffer
	errWriter := bufio.NewWriter(&errBuffer)
	cmd.SetStderr(errWriter)

	if err := cmd.Run(); err != nil {
		if !errorutil.IsExitStatusError(err) {
			return nil, err
		}
		return nil, errors.New(string(errBuffer.Bytes()))
	}

	return &jwt.Config{
		Email:      email,
		PrivateKey: outBuffer.Bytes(),
		TokenURL:   google.JWTTokenURL,
		Scopes:     []string{androidpublisher.AndroidpublisherScope},
	}, nil
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
			lanugage := matches[1]
			content, err := fileutil.ReadStringFromFile(recentChangesPath)
			if err != nil {
				return map[string]string{}, err
			}

			recentChangesMap[lanugage] = content
		}
	}

	return recentChangesMap, nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

func getKeyPath(keyPath string) (string, bool, error) {
	url, err := url.Parse(keyPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse key path, error: %s", err)
	}

	return strings.TrimPrefix(keyPath, "file://"), (url.Scheme == "http" || url.Scheme == "https"), nil
}

func main() {
	configs := createConfigsModelFromEnvs()

	fmt.Println()
	configs.print()

	if err := configs.validate(); err != nil {
		failf("Issue with input: %s", err)
	}

	//
	// Create client
	fmt.Println()
	log.Infof("Authenticating")

	jwtConfig := new(jwt.Config)

	if configs.JSONKeyPath != "" {

		jsonKeyPth, isRemote, err := getKeyPath(configs.JSONKeyPath)
		if err != nil {
			failf("Failed to get key path (%s), error: %s", configs.JSONKeyPath, err)
		}

		if isRemote {
			tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
			if err != nil {
				failf("Failed to create tmp dir, error: %s", err)
			}

			jsonKeySource := jsonKeyPth
			jsonKeyPth = filepath.Join(tmpDir, "key.json")
			if err := downloadFile(jsonKeySource, jsonKeyPth); err != nil {
				failf("Failed to download json key file, error: %s", err)
			}
		}

		authConfig, err := jwtConfigFromJSONKeyFile(jsonKeyPth)
		if err != nil {
			failf("Failed to create auth config from json key file, error: %s", err)
		}
		jwtConfig = authConfig
	} else {
		p12KeyPath, isRemote, err := getKeyPath(configs.P12KeyPath)
		if err != nil {
			failf("Failed to get key path (%s), error: %s", configs.P12KeyPath, err)
		}

		if isRemote {
			tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
			if err != nil {
				failf("Failed to create tmp dir, error: %s", err)
			}

			p12KeySource := p12KeyPath
			p12KeyPath = filepath.Join(tmpDir, "key.p12")
			if err := downloadFile(p12KeySource, p12KeyPath); err != nil {
				failf("Failed to download p12 key file, error: %s", err)
			}
		}

		authConfig, err := jwtConfigFromP12KeyFile(p12KeyPath, configs.ServiceAccountEmail)
		if err != nil {
			failf("Failed to create auth config from p12 key file, error: %s", err)
		}
		jwtConfig = authConfig
	}

	client := jwtConfig.Client(oauth2.NoContext)
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
	log.Infof("Upload apks")

	versionCodes := []int64{}
	apkPaths := strings.Split(configs.ApkPath, "|")
	for _, apkPath := range apkPaths {
		apkFile, err := os.Open(apkPath)
		if err != nil {
			failf("Failed to read apk (%s), error: %s", apkPath, err)
		}

		editsApksService := androidpublisher.NewEditsApksService(service)

		editsApksUloadCall := editsApksService.Upload(configs.PackageName, appEdit.Id)
		editsApksUloadCall.Media(apkFile, googleapi.ContentType("application/vnd.android.package-archive"))

		apk, err := editsApksUloadCall.Do()
		if err != nil {
			failf("Failed to upload apk, error: %s", err)
		}

		log.Printf(" uploaded apk version: %d", apk.VersionCode)
		versionCodes = append(versionCodes, apk.VersionCode)
	}
	// ---

	//
	// Update track
	fmt.Println()
	log.Infof("Update track")

	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newTrack := androidpublisher.Track{
		Track:        configs.Track,
		VersionCodes: versionCodes,
	}

	if configs.Track == rolloutTrackName {
		userFraction, err := strconv.ParseFloat(configs.UserFraction, 64)
		if err != nil {
			failf("Failed to parse user fraction, error: %s", err)
		}
		newTrack.UserFraction = userFraction
	}

	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, &newTrack)
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		failf("Failed to update track, error: %s", err)
	}

	log.Printf(" updated track: %s", track.Track)
	log.Printf(" assigned apk versions: %v", track.VersionCodes)
	// ---

	//
	// Deactivate blocking apks
	untrackApks := (configs.UntrackBlockingVersions == "true")

	if untrackApks && configs.Track == alphaTrackName {
		fmt.Println()
		log.Warnf("UntrackBlockingVersions is set, but selected track is: alpha, nothing to deactivate")
		untrackApks = false
	}

	if untrackApks && len(apkPaths) > 1 {
		fmt.Println()
		log.Warnf("UntrackBlockingVersions is set, but deploying multiple apks, deactivateing blocking versions is not supported, in this case")
		untrackApks = false
	}

	anyTrackUpdated := false

	if untrackApks {
		fmt.Println()
		log.Infof("Deactivating blocking apk versions")

		heighestVersion := versionCodes[0]

		// List all tracks
		tracksService := androidpublisher.NewEditsTracksService(service)

		// Collect tracks to update
		tracksListCall := tracksService.List(configs.PackageName, appEdit.Id)
		listResponse, err := tracksListCall.Do()
		if err != nil {
			failf("Failed to list tracks, error: %s", err)
		}

		tracks := listResponse.Tracks

		possibleTrackNamesToUpdate := []string{}
		switch configs.Track {
		case betaTrackName:
			possibleTrackNamesToUpdate = []string{alphaTrackName}
		case rolloutTrackName, productionTrackName:
			possibleTrackNamesToUpdate = []string{alphaTrackName, betaTrackName}
		}

		trackNamesToUpdate := []string{}
		for _, track := range tracks {
			for _, trackNameToUpdate := range possibleTrackNamesToUpdate {
				if trackNameToUpdate == track.Track {
					trackNamesToUpdate = append(trackNamesToUpdate, trackNameToUpdate)
				}
			}
		}

		log.Printf(" possible tracks to update: %v", trackNamesToUpdate)

		for _, trackName := range trackNamesToUpdate {
			tracksGetCall := tracksService.Get(configs.PackageName, appEdit.Id, trackName)
			track, err := tracksGetCall.Do()
			if err != nil {
				failf("Failed to get track (%s), error: %s", trackName, err)
			}

			log.Printf(" checking apk versions on track: %s", track.Track)

			versionCodesToKeep := []int64{}
			versionCodes := track.VersionCodes

			log.Infof(" versionCodes: %v", versionCodes)

			for _, versionCode := range versionCodes {
				if versionCode > heighestVersion {
					log.Donef(" - keeping apk with version: %d", versionCode)
					versionCodesToKeep = append(versionCodesToKeep, versionCode)
				} else {
					log.Warnf(" - removing apk with version: %d", versionCode)
				}
			}

			if len(versionCodes) != len(versionCodesToKeep) {
				anyTrackUpdated = true

				if len(versionCodesToKeep) > 0 {
					track.VersionCodes = versionCodesToKeep
				} else {
					track.VersionCodes = []int64{}
					track.NullFields = []string{"VersionCodes"}
				}

				track.ForceSendFields = []string{"VersionCodes"}

				tracksUpdateCall := tracksService.Patch(configs.PackageName, appEdit.Id, trackName, track)
				if _, err := tracksUpdateCall.Do(); err != nil && err != io.EOF {
					failf("Failed to update track (%s), error: %s", trackName, err)
				}
			}
		}

		if anyTrackUpdated {
			log.Donef("Desired versions deactivated")
		} else {
			log.Donef("No blocking apk version found")
		}
	}
	// ---

	//
	// Update listing
	if configs.WhatsnewsDir != "" {
		fmt.Println()
		log.Infof("Update listing")

		recentChangesMap, err := readLocalisedRecentChanges(configs.WhatsnewsDir)
		if err != nil {
			failf("Failed to read whatsnews, error: %s", err)
		}

		editsApklistingsService := androidpublisher.NewEditsApklistingsService(service)

		for _, versionCode := range versionCodes {
			log.Printf(" updating recent changes for version: %d", versionCode)

			for language, recentChanges := range recentChangesMap {
				newApkListing := androidpublisher.ApkListing{
					Language:      language,
					RecentChanges: recentChanges,
				}

				editsApkListingsCall := editsApklistingsService.Update(configs.PackageName, appEdit.Id, versionCode, language, &newApkListing)
				apkListing, err := editsApkListingsCall.Do()
				if err != nil {
					failf("Failed to update listing, error: %s", err)
				}

				log.Printf(" - language: %s", apkListing.Language)
			}
		}
	}
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
