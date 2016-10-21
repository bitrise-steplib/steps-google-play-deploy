package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/androidpublisher/v2"
	"google.golang.org/api/googleapi"

	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
)

func main() {
	//
	// Configs
	jsonKeyPth := "/Users/godrei/Downloads/key.json"
	packageName := "com.multipleabitest.godrei.multipleabitest"
	apkPath := "/Users/godrei/Downloads/apks/app-armeabi-v7a-release.apk|/Users/godrei/Downloads/apks/app-mips-release.apk|/Users/godrei/Downloads/apks/app-x86-release.apk"
	trackName := "beta"
	//

	//
	// Create client
	fmt.Println()
	log.Info("Create client")

	jsonKeyBytes, err := fileutil.ReadBytesFromFile(jsonKeyPth)
	if err != nil {
		log.Error("Failed to read json key, error: %s", err)
		os.Exit(1)
	}

	config, err := google.JWTConfigFromJSON(jsonKeyBytes, "https://www.googleapis.com/auth/androidpublisher")
	if err != nil {
		log.Error("Failed to create config, error: %s", err)
		os.Exit(1)
	}

	log.Detail(" config.Email: %s", config.Email)
	log.Detail(" config.Scopes: %s", config.Scopes)
	log.Detail(" config.TokenURL: %s", config.TokenURL)

	client := config.Client(oauth2.NoContext)
	service, err := androidpublisher.New(client)
	if err != nil {
		log.Error("Failed to create publisher service, error: %s", err)
		os.Exit(1)
	}
	// ---

	//
	// Create insert edit
	fmt.Println()
	log.Info("Create new edit")

	editsService := androidpublisher.NewEditsService(service)

	editsInsertCall := editsService.Insert(packageName, nil)

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
	log.Info("Edit apks upload")

	versionCode := []int64{}
	apkPaths := strings.Split(apkPath, "|")
	for _, apkPath := range apkPaths {
		apkFile, err := os.Open(apkPath)
		if err != nil {
			log.Error("Failed to read apk (%s), error: %s", apkPath, err)
			os.Exit(1)
		}

		editsApksService := androidpublisher.NewEditsApksService(service)

		editsApksUloadCall := editsApksService.Upload(packageName, appEdit.Id)
		editsApksUloadCall.Media(apkFile, googleapi.ContentType("application/vnd.android.package-archive"))

		apk, err := editsApksUloadCall.Do()
		if err != nil {
			log.Error("Failed to upload apk, error: %s", err)
			os.Exit(1)
		}

		log.Detail(" version: %d", apk.VersionCode)
		versionCode = append(versionCode, apk.VersionCode)
	}
	// ---

	//
	// Update track
	fmt.Println()
	log.Info("Update track")

	editsTracksService := androidpublisher.NewEditsTracksService(service)

	newTrack := androidpublisher.Track{
		Track:        trackName,
		UserFraction: 1.0,
		VersionCodes: versionCode,
	}

	editsTracksUpdateCall := editsTracksService.Update(packageName, appEdit.Id, trackName, &newTrack)
	track, err := editsTracksUpdateCall.Do()
	if err != nil {
		log.Error("Failed to update track, error: %s", err)
		os.Exit(1)
	}

	log.Detail(" track: %s", track.Track)
	log.Detail(" versions: %s", track.VersionCodes)
	// ---

	//
	// Commit edit
	editsCommitCall := editsService.Commit(packageName, appEdit.Id)
	appEdit, err = editsCommitCall.Do()
	if err != nil {
		log.Error("Failed to commit edit (%s), error: %s", appEdit.Id, err)
		os.Exit(1)
	}

	fmt.Println()
	log.Done("Edit committed")
	// ---
}
