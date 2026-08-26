package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
			c := Configs{Logger: log.NewLogger()}
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
				Logger:  log.NewLogger(),
			},
			wantApps:     nil,
			wantWarnings: nil,
		},
		{
			name: "prefers aab",
			config: Configs{
				AppPath: "app.apk|app.aab",
				Logger:  log.NewLogger(),
			},
			wantApps:     []string{"app.aab"},
			wantWarnings: []string{"Both .aab and .apk files provided, using the .aab file(s): app.aab"},
		},
		{
			name: "multiple .aab",
			config: Configs{
				AppPath: "app.aab\napp1.aab",
				Logger:  log.NewLogger(),
			},
			wantApps: []string{"app.aab", "app1.aab"},
		},
		{
			name: "unknown extension",
			config: Configs{
				AppPath: "mapping.txt",
				Logger:  log.NewLogger(),
			},
			wantApps:     nil,
			wantWarnings: []string{"unknown app path extension in path: mapping.txt, supported extensions: .apk, .aab"},
		},
		{
			name: "newline (\n) as a character",
			config: Configs{
				AppPath: `/bitrise/deploy/app-bitrise-signed.aab\n/bitrise/deploy/app.aab`,
				Logger:  log.NewLogger(),
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
			configs: Configs{Logger: log.NewLogger()},
			wantErr: false,
		},
		{
			name:        "single mapping file",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "single", "mapping.txt"), Logger: log.NewLogger()},
			wantErr:     false,
			createFiles: []string{filepath.Join(tmpDir, "single", "mapping.txt")},
		},
		{
			name:    "single non-existent mapping file",
			configs: Configs{MappingFile: filepath.Join(tmpDir, "single_nonexistent", "mapping.txt"), Logger: log.NewLogger()},
			wantErr: true,
		},
		{
			name:        "multiple existing mapping files",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "multiple", "mapping.txt") + "|" + filepath.Join(tmpDir, "multiple", "mapping2.txt"), Logger: log.NewLogger()},
			wantErr:     false,
			createFiles: []string{filepath.Join(tmpDir, "multiple", "mapping.txt"), filepath.Join(tmpDir, "multiple", "mapping2.txt")},
		},
		{
			name:        "1 existing 1 invalid mapping file",
			configs:     Configs{MappingFile: filepath.Join(tmpDir, "multiple_nonexistent", "mapping.txt") + "\n" + filepath.Join(tmpDir, "multiple_nonexistent", "mapping2.txt"), Logger: log.NewLogger()},
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
				Logger:            log.NewLogger(),
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

// The list syntax must match what validateMappingFile above accepts: the
// upload path used to split on `|` only.
func TestConfigs_mappingPaths_listSyntax(t *testing.T) {
	tests := []struct {
		name        string
		mappingFile string
		want        []string
	}{
		{
			name: "empty",
		},
		{
			name:        "single path",
			mappingFile: "/deploy/mapping.txt",
			want:        []string{"/deploy/mapping.txt"},
		},
		{
			name:        "pipe separated",
			mappingFile: "/deploy/mapping.txt|/deploy/mapping2.txt",
			want:        []string{"/deploy/mapping.txt", "/deploy/mapping2.txt"},
		},
		{
			name:        "newline separated",
			mappingFile: "/deploy/mapping.txt\n/deploy/mapping2.txt",
			want:        []string{"/deploy/mapping.txt", "/deploy/mapping2.txt"},
		},
		{
			name:        `newline (\n) as a character`,
			mappingFile: `/deploy/mapping.txt\n/deploy/mapping2.txt`,
			want:        []string{"/deploy/mapping.txt", "/deploy/mapping2.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Configs{MappingFile: tt.mappingFile, Logger: log.NewLogger()}
			if got := config.mappingPaths(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Configs.mappingPaths() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigs_appsToDeploy(t *testing.T) {
	tests := []struct {
		name        string
		appPath     string
		mappingFile string
		want        []appArtifact
	}{
		{
			name:        "pairs each app with the mapping file at the same position",
			appPath:     "app-demo.aab\napp-full.aab",
			mappingFile: "demo-mapping.txt\nfull-mapping.txt",
			want: []appArtifact{
				{path: "app-demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "app-full.aab", mappingPath: "full-mapping.txt"},
			},
		},
		{
			// dropping the .apk has to drop its mapping file too: pairing after
			// the .aab/.apk selection would upload the .apk's mapping with the
			// .aab, because the mapping list is still in its raw order
			name:        "dropping an apk drops its mapping file",
			appPath:     "app.apk\napp.aab",
			mappingFile: "apk-mapping.txt\naab-mapping.txt",
			want: []appArtifact{
				{path: "app.aab", mappingPath: "aab-mapping.txt"},
			},
		},
		{
			// the default configuration: app_path is
			// `$BITRISE_APK_PATH\n$BITRISE_AAB_PATH` and mapping_file is a single
			// $BITRISE_MAPPING_PATH, so the mapping belongs to the deploy, not to
			// the .apk that happens to be first in the list
			name:        "one mapping file for an apk and an aab",
			appPath:     "app.apk\napp.aab",
			mappingFile: "mapping.txt",
			want: []appArtifact{
				{path: "app.aab", mappingPath: "mapping.txt"},
			},
		},
		{
			name:    "apps without mapping files",
			appPath: "app-demo.aab\napp-full.aab",
			want: []appArtifact{
				{path: "app-demo.aab"},
				{path: "app-full.aab"},
			},
		},
		{
			name:        "fewer mapping files than apps",
			appPath:     "app-demo.aab\napp-full.aab",
			mappingFile: "demo-mapping.txt",
			want: []appArtifact{
				{path: "app-demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "app-full.aab"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Configs{AppPath: tt.appPath, MappingFile: tt.mappingFile, Logger: log.NewLogger()}
			got, _ := config.appsToDeploy()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Configs.appsToDeploy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigs_appsToDeploy_warnsOnExtraMappingFiles(t *testing.T) {
	config := Configs{
		AppPath:     "app.aab",
		MappingFile: "mapping.txt\nmapping2.txt",
		Logger:      log.NewLogger(),
	}

	_, warnings := config.appsToDeploy()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "More mapping files (2) provided than app files (1)") {
		t.Errorf("Configs.appsToDeploy() warnings = %v, want a warning about the extra mapping file", warnings)
	}
}
