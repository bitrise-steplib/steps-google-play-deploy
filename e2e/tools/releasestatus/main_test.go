package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/androidpublisher/v3"
)

func TestCheck(t *testing.T) {
	track := &androidpublisher.Track{Track: "internal", Releases: []*androidpublisher.TrackRelease{
		{Status: "completed", VersionCodes: []int64{10}},
		{Status: "draft", VersionCodes: []int64{11}},
	}}

	require.NoError(t, check(track, 11, "draft"))
	require.EqualError(t, check(track, 10, "draft"), "the release with version code 10 on the internal track is completed, want draft")
	require.EqualError(t, check(track, 12, "draft"), "no release on the internal track holds version code 12")
}
