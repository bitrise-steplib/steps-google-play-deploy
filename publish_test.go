package main

import (
	"reflect"
	"testing"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
)

func Test_expansionFiles(t *testing.T) {
	tests := []struct {
		name                    string
		appPaths                []string
		expansionFilePathConfig string
		entries                 []string
		wantErr                 bool
	}{
		{"mainOnly", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb|main:b.obb|main:c.obb", []string{"main:a.obb", "main:b.obb", "main:c.obb"}, false},
		{"pathOnly", []string{"x.apk", "y.apk", "z.apk"}, "patch:a.obb|patch:b.obb|patch:c.obb", []string{"patch:a.obb", "patch:b.obb", "patch:c.obb"}, false},
		{"mixed", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb|patch:b.obb|patch:c.obb", []string{"main:a.obb", "patch:b.obb", "patch:c.obb"}, false},
		{"omit", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb||patch:c.obb", []string{"main:a.obb", "", "patch:c.obb"}, false},
		{"multipleOmit", []string{"w.apk", "x.apk", "y.apk", "z.apk"}, "main:a.obb|||patch:c.obb", []string{"main:a.obb", "", "", "patch:c.obb"}, false},
		{"invalid", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb", []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expansionFiles(tt.appPaths, tt.expansionFilePathConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("expansionFiles() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.entries) {
				t.Errorf("expansionFiles() got1 = %v, want %v", got, tt.entries)
			}
		})
	}
}

func Test_hasShadowingVersions(t *testing.T) {
	tests := []struct {
		name                string
		currentVersionCodes googleapi.Int64s
		newVersionCodes     googleapi.Int64s
		want                bool
	}{
		{
			"currentIsHigher", googleapi.Int64s{5, 6, 7, 8}, googleapi.Int64s{1, 2, 3, 4}, false,
		},
		{
			"newIsHigher", googleapi.Int64s{5, 6, 7, 8}, googleapi.Int64s{9, 10, 11, 12}, true,
		},
		{
			"mixed", googleapi.Int64s{5, 6, 7, 8}, googleapi.Int64s{4, 6, 8, 10}, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasShadowingVersions(tt.currentVersionCodes, tt.newVersionCodes); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hasShadowingVersions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_releaseStatusFromConfig(t *testing.T) {

	tests := []struct {
		name         string
		userFraction float64
		want         string
	}{
		{"nonStagedRollout", 0, releaseStatusCompleted},
		{"nonStagedRollout", 0.5, releaseStatusInProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseStatusFromConfig(tt.userFraction); got != tt.want {
				t.Errorf("releaseStatusFromConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_expFileInfo(t *testing.T) {
	tests := []struct {
		name               string
		expFileConfigEntry string
		want               string
		want1              string
		wantErr            bool
	}{
		{"valid1", "main:/file/path/1.obb", "/file/path/1.obb", "main", false},
		{"valid2", "type:/file/path/2.obb", "/file/path/2.obb", "type", false},
		{"invalid1", "/file/path/2.obb", "", "", true},
		{"invalid2", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := expFileInfo(tt.expFileConfigEntry)
			if (err != nil) != tt.wantErr {
				t.Errorf("expFileInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("expFileInfo() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("expFileInfo() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_validateExpansionFilePath(t *testing.T) {
	tests := []struct {
		name        string
		expFilePath string
		want        bool
	}{
		{"valid1", "main:", true},
		{"valid2", "patch:", true},
		{"invalid1", "", false},
		{"invalid2", "something", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateExpansionFileConfig(tt.expFilePath); got != tt.want {
				t.Errorf("validateExpansionFileConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_removeBlockingVersionFromRelease(t *testing.T) {
	tests := []struct {
		name            string
		release         *androidpublisher.TrackRelease
		newVersionCodes googleapi.Int64s
		want            bool
	}{
		{"hasShadowing1", &androidpublisher.TrackRelease{nil, "releaseName", nil, "status", 0, googleapi.Int64s{1, 2, 3, 4}, nil, nil}, googleapi.Int64s{5, 6, 7, 8}, true},
		{"noShadowing1", &androidpublisher.TrackRelease{nil, "releaseName", nil, "status", 0, googleapi.Int64s{5, 6, 7, 8}, nil, nil}, googleapi.Int64s{1, 2, 3, 4}, false},
		{"differentNumberOfVersions1", &androidpublisher.TrackRelease{nil, "releaseName", nil, "status", 0, googleapi.Int64s{5}, nil, nil}, googleapi.Int64s{1, 2, 3, 4}, true},
		{"differentNumberOfVersions2", &androidpublisher.TrackRelease{nil, "releaseName", nil, "status", 0, googleapi.Int64s{5, 6, 7, 8}, nil, nil}, googleapi.Int64s{1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBlockingVersionInRelease(tt.release, tt.newVersionCodes); got != tt.want {
				t.Errorf("hasBlockingVersionInRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}
