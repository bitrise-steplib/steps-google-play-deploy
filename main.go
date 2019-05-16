package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/androidpublisher/v2"
	"google.golang.org/api/googleapi"

	"github.com/bitrise-io/go-steputils/stepconf"
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

// Configs ...
type Configs struct {
	JSONKeyPath             stepconf.Secret `env:"service_account_json_key_path,required"`
	PackageName             string          `env:"package_name,required"`
	AppPath                 string          `env:"app_path,required"`
	ExpansionfilePath       string          `env:"expansionfile_path"`
	Track                   string          `env:"track,required"`
	UserFraction            string          `env:"user_fraction,opt[0.05,0.1,0.2,0.5]"`
	WhatsnewsDir            string          `env:"whatsnews_dir"`
	MappingFile             string          `env:"mapping_file"`
	UntrackBlockingVersions string          `env:"untrack_blocking_versions,opt[true,false]"`

	// Deprecated
	ApkPath string `env:"apk_path"`

	// Internal fields, set by Configs.validateAndSelectApp
	APKs []string
	AAB  string
}

// Apps returns an AAB file path or APK file path(s) as an array.
func (c Configs) Apps() []string {
	// Config.validateAndSelectApp makes sure we either have an AAB or APK file(s)
	if len(c.AAB) > 0 {
		return []string{c.AAB}
	}
	return c.APKs
}

// ParseAppList parses the given app list and returns the APK and AAB file paths.
func ParseAppList(appList string) (apks []string, aabs []string, err error) {
	apps := strings.Split(appList, "\n")
	for _, app := range apps {
		app = strings.TrimSpace(app)
		if app == "" {
			continue
		}

		ext := filepath.Ext(app)
		switch ext {
		case ".aab":
			aabs = append(aabs, app)
		case ".apk":
			apks = append(apks, app)
		default:
			return nil, nil, fmt.Errorf("unknown app extension (%s) in path: %s, should be either .aab or .apk", ext, app)
		}
	}
	return
}

func (c *Configs) validateAndSelectApp() ([]string, error) {
	if strings.HasPrefix(string(c.JSONKeyPath), "file://") {
		pth := strings.TrimPrefix(string(c.JSONKeyPath), "file://")

		if exist, err := pathutil.IsPathExists(pth); err != nil {
			return nil, fmt.Errorf("failed to check if json key path exist at: %s, error: %s", pth, err)
		} else if !exist {
			return nil, errors.New("json key path not exist at: " + pth)
		}
	}

	apks, aabs, err := ParseAppList(c.AppPath)
	if err != nil {
		return nil, err
	}
	for _, pth := range append(apks, aabs...) {
		if exist, err := pathutil.IsPathExists(pth); err != nil {
			return nil, fmt.Errorf("failed to check if app exist at: %s, error: %s", pth, err)
		} else if !exist {
			return nil, errors.New("app not exist at: " + pth)
		}
	}

	var warnings []string
	if c.ApkPath != "" {
		warnings = append(warnings, "step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!")
	}

	if len(apks) == 0 && len(aabs) == 0 {
		if c.ApkPath != "" {
			warnings = append(warnings, "no app path provided via step input 'APK or App Bundle file path' (app_path), using deprecated step input 'APK file path' (apk_path)")

			pths := strings.Split(c.ApkPath, "|")
			for _, pth := range pths {
				if exist, err := pathutil.IsPathExists(pth); err != nil {
					return warnings, fmt.Errorf("failed to check if APK exist at: %s, error: %s", pth, err)
				} else if !exist {
					return warnings, errors.New("APK not exist at: " + pth)
				}
			}

			apks = pths
		}
	}

	if len(apks) == 0 && len(aabs) == 0 {
		return nil, errors.New("no android app provided for Google Play deploy, use 'APK or App Bundle file path' (app_path) to specify one")
	}

	if len(apks) > 0 && len(aabs) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("both APK (%s) and AAB (%s) files provided for Google Play deploy, using AAB file(s)", strings.Join(apks, ","), strings.Join(aabs, ",")))
		apks = nil
	}

	var aab string
	if len(aabs) > 0 {
		aab = aabs[0]
	}

	if len(aabs) > 1 {
		warnings = append(warnings, fmt.Sprintf("multiple AAB (%s) provided for Google Play deploy, using first: %s", strings.Join(aabs, ","), aab))
	}
	c.APKs = apks
	c.AAB = aab

	if c.WhatsnewsDir != "" {
		if exist, err := pathutil.IsDirExists(c.WhatsnewsDir); err != nil {
			return warnings, fmt.Errorf("failed to check if what's new directory exist at: %s, error: %s", c.WhatsnewsDir, err)
		} else if !exist {
			return warnings, errors.New("what's new directory not exist at: " + c.WhatsnewsDir)
		}
	}

	if c.MappingFile != "" {
		if exist, err := pathutil.IsPathExists(c.MappingFile); err != nil {
			return warnings, fmt.Errorf("Failed to check if mapping file exist at: %s, error: %s", c.MappingFile, err)
		} else if !exist {
			return warnings, errors.New("mapping file not exist at: " + c.MappingFile)
		}
	}

	return warnings, nil
}

// parseAppList parses the given app list and returns the APK and AAB file paths.
func parseAppList(appList string) (apks []string, aabs []string, err error) {
	apps := strings.Split(appList, "\n")
	for _, app := range apps {
		app = strings.TrimSpace(app)
		if app == "" {
			continue
		}

		ext := filepath.Ext(app)
		switch ext {
		case ".aab":
			aabs = append(aabs, app)
		case ".apk":
			apks = append(apks, app)
		default:
			return nil, nil, fmt.Errorf("unknown app extension (%s) in path: %s, should be either .aab or .apk", ext, app)
		}
	}
	return
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

func parseURI(keyPath string) (string, bool, error) {
	url, err := url.Parse(keyPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse url (%s), error: %s", keyPath, err)
	}

	return strings.TrimPrefix(keyPath, "file://"), (url.Scheme == "http" || url.Scheme == "https"), nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

func main() {
	var configs Configs
	if err := stepconf.Parse(&configs); err != nil {
		failf("Couldn't create config: %s\n", err)
	}
	fmt.Println(configs)

	warnings, err := configs.validateAndSelectApp()
	for _, warning := range warnings {
		log.Warnf(warning)
	}
	if err != nil {
		failf(err.Error())
	}

	//
	// Create client
	fmt.Println()
	log.Infof("Authenticating")

	jwtConfig := new(jwt.Config)
	jsonKeyPth, isRemote, err := parseURI(string(configs.JSONKeyPath))
	if err != nil {
		failf("Failed to prepare key path (%s), error: %s", configs.JSONKeyPath, err)
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
	// List track infos
	fmt.Println()
	log.Infof("List track infos")

	tracksService := androidpublisher.NewEditsTracksService(service)
	tracksListCall := tracksService.List(configs.PackageName, appEdit.Id)
	listResponse, err := tracksListCall.Do()
	if err != nil {
		failf("Failed to list tracks, error: %s", err)
	}
	for _, track := range listResponse.Tracks {
		log.Printf(" %s versionCodes: %v", track.Track, track.VersionCodes)
	}

	//
	// Upload APKs
	fmt.Println()
	log.Infof("Upload apks or app bundle")

	versionCodes := []int64{}
	appPaths := configs.Apps()

	// "main:/file/path/1.obb|patch:/file/path/2.obb"
	expansionfileUpload := strings.TrimSpace(configs.ExpansionfilePath) != ""
	expansionfilePaths := strings.Split(configs.ExpansionfilePath, "|")

	if expansionfileUpload && (len(appPaths) != len(expansionfilePaths)) {
		failf("Mismatching number of APKs(%d) and Expansionfiles(%d)", len(appPaths), len(expansionfilePaths))
	}

	for i, appPath := range appPaths {
		versionCode := int64(0)
		apkFile, err := os.Open(appPath)
		if err != nil {
			failf("Failed to read apk (%s), error: %s", appPath, err)
		}

		if filepath.Ext(appPath) == ".aab" {
			editsBundlesService := androidpublisher.NewEditsBundlesService(service)

			editsBundlesUploadCall := editsBundlesService.Upload(configs.PackageName, appEdit.Id)
			editsBundlesUploadCall.Media(apkFile, googleapi.ContentType("application/octet-stream"))

			bundle, err := editsBundlesUploadCall.Do()
			if err != nil {
				failf("Failed to upload app bundle, error: %s", err)
			}
			log.Printf(" uploaded app bundle version: %d", bundle.VersionCode)
			versionCodes = append(versionCodes, bundle.VersionCode)
			versionCode = bundle.VersionCode
		} else {
			editsApksService := androidpublisher.NewEditsApksService(service)

			editsApksUploadCall := editsApksService.Upload(configs.PackageName, appEdit.Id)
			editsApksUploadCall.Media(apkFile, googleapi.ContentType("application/vnd.android.package-archive"))

			apk, err := editsApksUploadCall.Do()
			if err != nil {
				failf("Failed to upload apk, error: %s", err)
			}

			log.Printf(" uploaded apk version: %d", apk.VersionCode)
			versionCodes = append(versionCodes, apk.VersionCode)
			versionCode = apk.VersionCode

			if expansionfileUpload {
				// "main:/file/path/1.obb"
				cleanExpfilePath := strings.TrimSpace(expansionfilePaths[i])
				if !strings.HasPrefix(cleanExpfilePath, "main:") && !strings.HasPrefix(cleanExpfilePath, "patch:") {
					failf("Invalid expansion file config: %s", expansionfilePaths[i])
				}

				// [0]: "main" [1]:"/file/path/1.obb"
				expansionfilePathSplit := strings.Split(cleanExpfilePath, ":")

				// "main"
				expfileType := strings.TrimSpace(expansionfilePathSplit[0])

				// "/file/path/1.obb"
				expfilePth := strings.TrimSpace(strings.Join(expansionfilePathSplit[1:], ""))

				expansionFile, err := os.Open(expfilePth)
				if err != nil {
					failf("Failed to read expansion file (%s), error: %s", expansionFile, err)
				}
				editsExpansionfilesService := androidpublisher.NewEditsExpansionfilesService(service)
				editsExpansionfilesCall := editsExpansionfilesService.Upload(configs.PackageName, appEdit.Id, versionCode, expfileType)
				editsExpansionfilesCall.Media(expansionFile, googleapi.ContentType("application/vnd.android.package-archive"))
				if _, err := editsExpansionfilesCall.Do(); err != nil {
					failf("Failed to upload expansion file, error: %s", err)
				}
			}
		}

		// Upload mapping.txt
		if configs.MappingFile != "" && versionCode != 0 {
			mappingFile, err := os.Open(configs.MappingFile)
			if err != nil {
				failf("Failed to read mapping file (%s), error: %s", configs.MappingFile, err)
			}
			editsDeobfuscationfilesService := androidpublisher.NewEditsDeobfuscationfilesService(service)
			editsDeobfuscationfilesUloadCall := editsDeobfuscationfilesService.Upload(configs.PackageName, appEdit.Id, versionCode, "proguard")
			editsDeobfuscationfilesUloadCall.Media(mappingFile, googleapi.ContentType("application/octet-stream"))

			if _, err = editsDeobfuscationfilesUloadCall.Do(); err != nil {
				failf("Failed to upload mapping file, error: %s", err)
			}

			log.Printf(" uploaded mapping file for apk version: %d", versionCode)
			if i < len(appPaths)-1 {
				fmt.Println()
			}
		}
	}

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

	anyTrackUpdated := false

	if untrackApks {
		fmt.Println()
		log.Infof("Deactivating blocking apk versions")

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

			log.Infof(" versionCodes: %v", track.VersionCodes)

			var cleanTrack bool

			if len(track.VersionCodes) != len(versionCodes) {
				log.Warnf("Mismatching apk count, removing (%v) versions from track: %s", track.VersionCodes, track.Track)
				cleanTrack = true
			} else {
				sort.Slice(track.VersionCodes, func(a, b int) bool { return track.VersionCodes[a] < track.VersionCodes[b] })
				sort.Slice(versionCodes, func(a, b int) bool { return versionCodes[a] < versionCodes[b] })

				for i := 0; i < len(versionCodes); i++ {
					if track.VersionCodes[i] < versionCodes[i] {
						log.Warnf("Shadowing APK found, removing (%v) versions from track: %s", track.VersionCodes, track.Track)
						cleanTrack = true
						break
					}
				}
			}

			if cleanTrack {
				anyTrackUpdated = true

				track.VersionCodes = []int64{}
				track.NullFields = []string{"VersionCodes"}
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
