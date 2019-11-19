package main

import (
	"fmt"

	"google.golang.org/api/androidpublisher/v3"
)

func trackToString(track *androidpublisher.Track) string {
	s := fmt.Sprintf("%s track:\n", track.Track)
	for i, release := range track.Releases {
		s += fmt.Sprintf("- %s", releaseToString(release))
		if i != len(track.Releases)-1 {
			s += "\n"
		}
	}
	return s
}

func releaseToString(release *androidpublisher.TrackRelease) string {
	return fmt.Sprintf("'%s' release versionCodes: %v, status: '%v'", release.Name, release.VersionCodes, release.Status)
}
