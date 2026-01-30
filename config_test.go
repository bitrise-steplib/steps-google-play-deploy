package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/v2/log"
)

func Test_fraction(t *testing.T) {
	type cfgs struct {
		UserFraction float64 `env:"user_fraction,range]0.0..1.0["`
		Input        string
		Value        float64
		WantErr      bool
	}

	for _, cfg := range []cfgs{
		{
			Input:   "",
			Value:   0,
			WantErr: false,
		},
		{
			Input:   "0.3",
			Value:   0.3,
			WantErr: false,
		},
		{
			Input:   "0",
			Value:   0,
			WantErr: true,
		},
	} {
		if err := os.Setenv("user_fraction", cfg.Input); err != nil {
			t.Fatal(err)
		}

		if err := stepconf.Parse(&cfg); err != nil && !cfg.WantErr {
			t.Fatal(err)
		}

		if cfg.UserFraction != cfg.Value {
			t.Fatal("eeeh man")
		}
	}
}

func Test_parseInputList(t *testing.T) {
	tests := []struct {
		name     string
		list     string
		wantApps []string
	}{
		{
			name:     "empty app list",
			list:     "",
			wantApps: nil,
		},
		{
			name:     "newline separated list",
			list:     "app.apk\napp.aab\n \n",
			wantApps: []string{"app.apk", "app.aab"},
		},
		{
			name:     "pipe separated list",
			list:     "|app.apk|app.aab|",
			wantApps: []string{"app.apk", "app.aab"},
		},
		{
			name:     "pipe and newline separated list",
			list:     "\napp1.apk|app2.apk\napp.aab|",
			wantApps: []string{"app1.apk", "app2.apk", "app.aab"},
		},
		{
			name:     "pipe and newline separated list",
			list:     "/bitrise/deploy/app-bitrise-signed.aab\n/bitrise/deploy/app.aab",
			wantApps: []string{"/bitrise/deploy/app-bitrise-signed.aab", "/bitrise/deploy/app.aab"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Configs{logger: log.NewLogger()}
			if gotApps := c.parseInputList(tt.list); !reflect.DeepEqual(gotApps, tt.wantApps) {
				t.Errorf("parseInputList() = %v, want %v", gotApps, tt.wantApps)
			}
		})
	}
}

func TestConfigs_appPaths(t *testing.T) {
	tests := []struct {
		name         string
		config       Configs
		wantApps     []string
		wantWarnings []string
	}{
		{
			name: "empty test",
			config: Configs{
				AppPath: "",
				logger:  log.NewLogger(),
			},
			wantApps:     nil,
			wantWarnings: nil,
		},
		{
			name: "prefers aab",
			config: Configs{
				AppPath: "app.apk|app.aab",
				logger:  log.NewLogger(),
			},
			wantApps:     []string{"app.aab"},
			wantWarnings: []string{"Both .aab and .apk files provided, using the .aab file(s): app.aab"},
		},
		{
			name: "multiple .aab",
			config: Configs{
				AppPath: "app.aab\napp1.aab",
				logger:  log.NewLogger(),
			},
			wantApps: []string{"app.aab", "app1.aab"},
		},
		{
			name: "unknown extension",
			config: Configs{
				AppPath: "mapping.txt",
				logger:  log.NewLogger(),
			},
			wantApps:     nil,
			wantWarnings: []string{"unknown app path extension in path: mapping.txt, supported extensions: .apk, .aab"},
		},
		{
			name: "newline (\n) as a character",
			config: Configs{
				AppPath: `/bitrise/deploy/app-bitrise-signed.aab\n/bitrise/deploy/app.aab`,
				logger:  log.NewLogger(),
			},
			wantApps: []string{"/bitrise/deploy/app-bitrise-signed.aab", "/bitrise/deploy/app.aab"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotApps, gotWarnings := tt.config.appPaths()
			if !reflect.DeepEqual(gotApps, tt.wantApps) {
				t.Errorf("Configs.appPaths() gotApps = %v, want %v", gotApps, tt.wantApps)
			}
			if !reflect.DeepEqual(gotWarnings, tt.wantWarnings) {
				t.Errorf("Configs.appPaths() gotWarnings = %v, want %v", gotWarnings, tt.wantWarnings)
			}
		})
	}
}

func TestConfigs_mappingPaths(t *testing.T) {
	tmpDir := t.TempDir()
	tests := []struct {
		name        string
		configs     Configs
		wantErr     bool
		createFiles []string
	}{
		{
			name:    "no mapping file",
			configs: Configs{logger: log.NewLogger()},
			wantErr: false,
		},
		{
			name:        "single mapping file",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "single", "mapping.txt"), logger: log.NewLogger()},
			wantErr:     false,
			createFiles: []string{filepath.Join(tmpDir, "single", "mapping.txt")},
		},
		{
			name:    "single non-existent mapping file",
			configs: Configs{MappingFile: filepath.Join(tmpDir, "single_nonexistent", "mapping.txt"), logger: log.NewLogger()},
			wantErr: true,
		},
		{
			name:        "multiple existing mapping files",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "multiple", "mapping.txt") + "|" + filepath.Join(tmpDir, "multiple", "mapping2.txt"), logger: log.NewLogger()},
			wantErr:     false,
			createFiles: []string{filepath.Join(tmpDir, "multiple", "mapping.txt"), filepath.Join(tmpDir, "multiple", "mapping2.txt")},
		},
		{
			name:        "1 existing 1 invalid mapping file",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "multiple_nonexistent", "mapping.txt") + "\n" + filepath.Join(tmpDir, "multiple_nonexistent", "mapping2.txt"), logger: log.NewLogger()},
			wantErr:     true,
			createFiles: []string{filepath.Join(tmpDir, "multiple_nonexistent", "mapping.txt")},
		},
	}

	for _, tt := range tests {
		for _, path := range tt.createFiles {
			err := os.MkdirAll(filepath.Dir(path), os.ModePerm)
			if err != nil {
				t.Errorf("failed to create path: %s", err)
			}
			_, err = os.Create(path)
			if err != nil {
				t.Errorf("failed to create file: %s", err)
			}
		}

		gotErr := tt.configs.validateMappingFile()

		if tt.wantErr && gotErr == nil {
			t.Errorf("%s: wanted error but result is nil", tt.name)
		} else if !tt.wantErr && gotErr != nil {
			t.Errorf("%s: wanted no error, got: %v", tt.name, gotErr)
		}
	}
}

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
		{"invalid1", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb", []string{}, true},
		{"invalid2", []string{"x.apk", "y.apk", "z.apk"}, "", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Configs{
				ExpansionfilePath: tt.expansionFilePathConfig,
				logger:            log.NewLogger(),
			}
			got, err := c.expansionFiles(tt.appPaths)
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
