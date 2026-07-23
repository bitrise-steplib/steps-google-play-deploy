package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bitrise-io/go-utils/v2/log"
)

func TestConfigs_appsToDeploy_warnsWhenMoreMappingsThanApps(t *testing.T) {
	c := Configs{AppPath: "a.aab|b.aab", MappingFile: "m1|m2|m3", Logger: log.NewLogger()}

	_, warnings := c.appsToDeploy()

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "More mapping files") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about more mapping files than app files, got %v", warnings)
	}
}

func TestConfigs_appsToDeploy_pairsMappingsByPosition(t *testing.T) {
	tests := []struct {
		name        string
		appPath     string
		mappingFile string
		want        []appArtifact
	}{
		{
			name:        "aligns each app with its mapping by index",
			appPath:     "demo.aab|prod.aab",
			mappingFile: "demo-mapping.txt|prod-mapping.txt",
			want: []appArtifact{
				{path: "demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "prod.aab", mappingPath: "prod-mapping.txt"},
			},
		},
		{
			name:        "empty placeholder keeps following mappings aligned",
			appPath:     "demo.aab|debug.aab|prod.aab",
			mappingFile: "demo-mapping.txt||prod-mapping.txt",
			want: []appArtifact{
				{path: "demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "debug.aab", mappingPath: ""},
				{path: "prod.aab", mappingPath: "prod-mapping.txt"},
			},
		},
		{
			name:        "fewer mappings than apps leaves trailing apps without a mapping",
			appPath:     "demo.aab|prod.aab",
			mappingFile: "demo-mapping.txt",
			want: []appArtifact{
				{path: "demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "prod.aab", mappingPath: ""},
			},
		},
		{
			name:        "AAB-wins drops the APK together with its mapping, keeping AAB pairs aligned",
			appPath:     "demo.apk|demo.aab|prod.aab",
			mappingFile: "apk-mapping.txt|demo-mapping.txt|prod-mapping.txt",
			want: []appArtifact{
				{path: "demo.aab", mappingPath: "demo-mapping.txt"},
				{path: "prod.aab", mappingPath: "prod-mapping.txt"},
			},
		},
		{
			name:        "no mapping file at all",
			appPath:     "demo.aab|prod.aab",
			mappingFile: "",
			want: []appArtifact{
				{path: "demo.aab", mappingPath: ""},
				{path: "prod.aab", mappingPath: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Configs{AppPath: tt.appPath, MappingFile: tt.mappingFile, Logger: log.NewLogger()}
			got, _ := c.appsToDeploy()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("appsToDeploy() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
