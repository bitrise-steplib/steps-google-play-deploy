package main

import (
	"github.com/bitrise-io/go-utils/log"
	"google.golang.org/api/androidpublisher/v3"
)

// printTrack prints out the given track to the console.
func printTrack(track *androidpublisher.Track, prefix string) {
	if prefix != "" {
		log.Infof("%s", prefix)
	}
	log.Infof("%s", track.Track)
	for _, release := range track.Releases {
		printRelease(*release)
	}
}

// printRelease prints out the given release to the console.
func printRelease(release androidpublisher.TrackRelease) {
	log.Infof("Release '%s' has versionCodes: '%v', status: '%v", release.Name, release.VersionCodes, release.Status)
}
