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

	"github.com/bitrise-io/go-utils/cmdex"
	"github.com/bitrise-io/go-utils/errorutil"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"
)

// ConfigsModel ...
type ConfigsModel struct {
	ServiceAccountEmail string
	P12KeyPath          string
	JSONKeyPath         string
	PackageName         string
	ApkPath             string
	Track               string
	UserFraction        string
	WhatsnewsDir        string
}

func createConfigsModelFromEnvs() ConfigsModel {
	return ConfigsModel{
		ServiceAccountEmail: os.Getenv("service_account_email"),
		P12KeyPath:          os.Getenv("key_file_path"),
		JSONKeyPath:         os.Getenv("service_account_json_key_path"),
		PackageName:         os.Getenv("package_name"),
		ApkPath:             os.Getenv("apk_path"),
		Track:               os.Getenv("track"),
		UserFraction:        os.Getenv("user_fraction"),
		WhatsnewsDir:        os.Getenv("whatsnews_dir"),
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
	log.Info("Configs:")
	log.Detail("- JSONKeyPath: %s", secureInput(configs.JSONKeyPath))
	log.Detail("- PackageName: %s", configs.PackageName)
	log.Detail("- ApkPath: %s", configs.ApkPath)
	log.Detail("- Track: %s", configs.Track)
	log.Detail("- UserFraction: %s", configs.UserFraction)
	log.Detail("- WhatsnewsDir: %s", configs.WhatsnewsDir)
	log.Info("Deprecated Configs:")
	log.Detail("- ServiceAccountEmail: %s", secureInput(configs.ServiceAccountEmail))
	log.Detail("- P12KeyPath: %s", secureInput(configs.P12KeyPath))
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

	if configs.Track == "rollout" {
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
			log.Warn("Failed to close (%s)", targetPath)
		}
	}()

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download from (%s), error: %s", downloadURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warn("failed to close (%s) body", downloadURL)
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
	cmd := cmdex.NewCommand("openssl", "pkcs12", "-in", pth, "-passin", "pass:notasecret", "-nodes")

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

func main() {
	configs := createConfigsModelFromEnvs()

	fmt.Println()
	configs.print()

	if err := configs.validate(); err != nil {
		fmt.Println()
		log.Error("Issue with input: %s", err)

		os.Exit(1)
	}

	//
	// Create client
	fmt.Println()
	log.Info("Authenticateing")

	jwtConfig := new(jwt.Config)

	if configs.JSONKeyPath != "" {
		jsonKeyPth := ""

		if strings.HasPrefix(configs.JSONKeyPath, "file://") {
			jsonKeyPth = strings.TrimPrefix(configs.JSONKeyPath, "file://")
		} else {
			tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
			if err != nil {
				log.Error("Failed to create tmp dir, error: %s", err)
				os.Exit(1)
			}

			jsonKeyPth = filepath.Join(tmpDir, "key.json")

			if err := downloadFile(configs.JSONKeyPath, jsonKeyPth); err != nil {
				log.Error("Failed to download json key file, error: %s", err)
				os.Exit(1)
			}
		}

		authConfig, err := jwtConfigFromJSONKeyFile(jsonKeyPth)
		if err != nil {
			log.Error("Failed to create auth config from json key file, error: %s", err)
			os.Exit(1)
		}
		jwtConfig = authConfig
	} else {
		p12KeyPath := ""

		if strings.HasPrefix(configs.P12KeyPath, "file://") {
			p12KeyPath = strings.TrimPrefix(configs.P12KeyPath, "file://")
		} else {
			tmpDir, err := pathutil.NormalizedOSTempDirPath("__google-play-deploy__")
			if err != nil {
				log.Error("Failed to create tmp dir, error: %s", err)
				os.Exit(1)
			}

			p12KeyPath = filepath.Join(tmpDir, "key.p12")

			if err := downloadFile(configs.P12KeyPath, p12KeyPath); err != nil {
				log.Error("Failed to download p12 key file, error: %s", err)
				os.Exit(1)
			}
		}

		authConfig, err := jwtConfigFromP12KeyFile(p12KeyPath, configs.ServiceAccountEmail)
		if err != nil {
			log.Error("Failed to create auth config from p12 key file, error: %s", err)
			os.Exit(1)
		}
		jwtConfig = authConfig
	}

	client := jwtConfig.Client(oauth2.NoContext)
	service, err := androidpublisher.New(client)
	if err != nil {
		log.Error("Failed to create publisher service, error: %s", err)
		os.Exit(1)
	}

	log.Done("Authenticated client created")
	// ---

	//
	// Create insert edit
	fmt.Println()
	log.Info("Create new edit")

	editsService := androidpublisher.NewEditsService(service)

	editsInsertCall := editsService.Insert(configs.PackageName, nil)

	appEdit, err := editsInsertCall.Do()
	if err != nil {
		log.Error("Failed to perform edit insert call, error: %s", err)
		os.Exit(1)
	}

	log.Detail(" editID: %s", appEdit.Id)
	// ---

	//
	// Upload APKs
	fmt.Println()
	log.Info("Upload apks")

	versionCodes := []int64{}
	apkPaths := strings.Split(configs.ApkPath, "|")
	for _, apkPath := range apkPaths {
		apkFile, err := os.Open(apkPath)
		if err != nil {
			log.Error("Failed to read apk (%s), error: %s", apkPath, err)
			os.Exit(1)
		}

		editsApksService := androidpublisher.NewEditsApksService(service)

		editsApksUloadCall := editsApksService.Upload(configs.PackageName, appEdit.Id)
		editsApksUloadCall.Media(apkFile, googleapi.ContentType("application/vnd.android.package-archive"))

		apk, err := editsApksUloadCall.Do()
		if err != nil {
			log.Error("Failed to upload apk, error: %s", err)
			os.Exit(1)
		}

		log.Detail(" uploaded apk version: %d", apk.VersionCode)
		versionCodes = append(versionCodes, apk.VersionCode)
	}
	// ---

	//
	// Update track
	fmt.Println()
	log.Info("Update track")

	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newTrack := androidpublisher.Track{
		Track:        configs.Track,
		VersionCodes: versionCodes,
	}

	if configs.Track == "rollout" {
		userFraction, err := strconv.ParseFloat(configs.UserFraction, 64)
		if err != nil {
			log.Error("Failed to parse user fraction, error: %s", err)
			os.Exit(1)
		}
		newTrack.UserFraction = userFraction
	}

	editsTracksUpdateCall := editsTracksService.Update(configs.PackageName, appEdit.Id, configs.Track, &newTrack)
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		log.Error("Failed to update track, error: %s", err)
		os.Exit(1)
	}

	log.Detail(" updated track: %s", track.Track)
	log.Detail(" assigned apk versions: %v", track.VersionCodes)
	// ---

	//
	// Update listing
	if configs.WhatsnewsDir != "" {
		fmt.Println()
		log.Info("Update listing")

		recentChangesMap, err := readLocalisedRecentChanges(configs.WhatsnewsDir)
		if err != nil {
			log.Error("Failed to read whatsnews, error: %s", err)
			os.Exit(1)
		}

		editsApklistingsService := androidpublisher.NewEditsApklistingsService(service)

		for _, versionCode := range versionCodes {
			log.Detail(" updating recent changes for version: %d", versionCode)

			for language, recentChanges := range recentChangesMap {
				newApkListing := androidpublisher.ApkListing{
					Language:      language,
					RecentChanges: recentChanges,
				}

				editsApkListingsCall := editsApklistingsService.Update(configs.PackageName, appEdit.Id, versionCode, language, &newApkListing)
				apkListing, err := editsApkListingsCall.Do()
				if err != nil {
					log.Error("Failed to update listing, error: %s", err)
					os.Exit(1)
				}

				log.Detail(" - language: %s", apkListing.Language)
			}
		}
	}
	// ---

	//
	// Validate edit
	fmt.Println()
	log.Info("Validating edit")

	editsValidateCall := editsService.Validate(configs.PackageName, appEdit.Id)
	if _, err := editsValidateCall.Do(); err != nil {
		log.Error("Failed to validate edit, error: %s", err)
		os.Exit(1)
	}

	log.Done("Edit is valid")
	// ---

	//
	// Commit edit
	fmt.Println()
	log.Info("Committing edit")

	editsCommitCall := editsService.Commit(configs.PackageName, appEdit.Id)
	if _, err := editsCommitCall.Do(); err != nil {
		log.Error("Failed to commit edit, error: %s", err)
		os.Exit(1)
	}

	log.Done("Edit committed")
	// ---
}
