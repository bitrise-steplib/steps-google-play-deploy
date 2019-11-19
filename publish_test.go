package main

import (
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
