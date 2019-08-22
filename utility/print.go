package utility

import (
	"github.com/bitrise-io/go-utils/log"
	"google.golang.org/api/androidpublisher/v3"
)

// PrintTrack prints out the given track to the console.
func PrintTrack(track *androidpublisher.Track, prefix string) {
	if prefix != "" {
		log.Infof("%s", prefix)
	}
	log.Infof("%s", track.Track)
	for _, release := range track.Releases {
		PrintRelease(*release)
	}
}

// PrintRelease prints out the given release to the console.
func PrintRelease(release androidpublisher.TrackRelease) {
	log.Infof("Release '%s' has versionCodes: %v", release.Name, release.VersionCodes)
}
