package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
		tmpDir, err := ioutil.TempDir("", "Test_readLocalisedRecentChanges")
		if err != nil {
			return "", err
		}

		for locale, notes := range localeToNote {
			if err := ioutil.WriteFile(filepath.Join(tmpDir, "whatsnew-"+locale), []byte(notes), 0600); err != nil {
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
