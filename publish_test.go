package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_verifyStatusOfTheCreatedRelease(t *testing.T) {
	tests := []struct {
		name           string
		config         Configs
		expectedStatus string
	}{
		{
			"Given the user fraction is equal to 0 and the status is not set when the release is created then expect the status to be COMPLETED",
			Configs{UserFraction: 0}, releaseStatusCompleted,
		},
		{
			"Given the user fraction is greather than 0 and the status is not set when the release is created then expect the status to be IN_PROGRESS",
			Configs{UserFraction: 0.5}, releaseStatusInProgress,
		},
		{
			"Given the status when the release is created then expect the status to be the same as in the config",
			Configs{Status: releaseStatusDraft}, releaseStatusDraft,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trackRelease, err := createTrackRelease(tt.config, []int64{})

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, trackRelease.Status)
		})
	}
}

func Test_verifyUserFractionOfTheCreatedRelease(t *testing.T) {
	tests := []struct {
		name                 string
		config               Configs
		expectedUserFraction float64
	}{
		{
			"Given status is IN_PROGRESS and the user fraction is set when the release is created then expect the user fraction to be applied",
			Configs{UserFraction: 1, Status: releaseStatusInProgress}, 1,
		},
		{
			"Given status is HALTED and the user fraction is set when the release is created then expect the user fraction to be applied",
			Configs{UserFraction: 1, Status: releaseStatusHalted}, 1,
		},
		{
			"Given status is DRAFT and the user fraction is set when the release is created then expect the user fraction not to be applied",
			Configs{UserFraction: 1, Status: releaseStatusDraft}, 0,
		},
		{
			"Given status is COMPLETED and the user fraction is set when the release is created then expect the user fraction not to be applied",
			Configs{UserFraction: 1, Status: releaseStatusCompleted}, 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trackRelease, err := createTrackRelease(tt.config, []int64{})

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedUserFraction, trackRelease.UserFraction)
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

func Test_readLocalisedRecentChanges(t *testing.T) {
	createTestFiles := func(localeToNote map[string]string) (string, error) {
		tmpDir := t.TempDir()

		for locale, notes := range localeToNote {
			if err := os.WriteFile(filepath.Join(tmpDir, "whatsnew-"+locale), []byte(notes), 0600); err != nil {
				return "", err
			}
		}
		return tmpDir, nil
	}

	tests := []struct {
		name      string
		testFiles map[string]string
		want      map[string]string
		wantErr   bool
	}{
		{
			name:      "1 language: en-US",
			testFiles: map[string]string{"en-US": "English"},
			want:      map[string]string{"en-US": "English"},
			wantErr:   false,
		},
		{
			name:      "2 language: en-US",
			testFiles: map[string]string{"en-US": "English", "de-DE": "German"},
			want:      map[string]string{"en-US": "English", "de-DE": "German"},
			wantErr:   false,
		},
		{
			name:      "no second subtag",
			testFiles: map[string]string{"ca": "Catalan"},
			want:      map[string]string{"ca": "Catalan"},
			wantErr:   false,
		},
		{
			name:      "Latin American Spanish",
			testFiles: map[string]string{"es-419": "Latin American Spanish"},
			want:      map[string]string{"es-419": "Latin American Spanish"},
			wantErr:   false,
		},
		{
			// "sr-Latn-RS" represents Serbian ('sr') written using Latin script
			//('Latn') as used in Serbia ('RS').
			name:      "Latin American Spanish",
			testFiles: map[string]string{"sr-Latn-RS": "Serbian"},
			want:      map[string]string{"sr-Latn-RS": "Serbian"},
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir, err := createTestFiles(tt.testFiles)
			if err != nil {
				t.Fatalf("setup: failed to create test files, error: %s", err)
			}
			defer func() {
				err := os.RemoveAll(testDir)
				if err != nil {
					t.Logf("Faield to remove test dir, error: %s", err)
				}
			}()

			got, err := readLocalisedRecentChanges(testDir)
			if (err != nil) != tt.wantErr {
				t.Errorf("readLocalisedRecentChanges() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readLocalisedRecentChanges() = %v, want %v", got, tt.want)
			}
		})
	}
}
