package utility

import (
	"reflect"
	"testing"
)

func TestGetExpansionFiles(t *testing.T) {
	tests := []struct {
		name                    string
		appPaths                []string
		expansionFilePathConfig string
		toUpload                bool
		entries                 []string
		wantErr                 bool
	}{
		{"mainOnly", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb|main:b.obb|main:c.obb", true, []string{"main:a.obb", "main:b.obb", "main:c.obb"}, false},
		{"pathOnly", []string{"x.apk", "y.apk", "z.apk"}, "patch:a.obb|patch:b.obb|patch:c.obb", true, []string{"patch:a.obb", "patch:b.obb", "patch:c.obb"}, false},
		{"mixed", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb|patch:b.obb|patch:c.obb", true, []string{"main:a.obb", "patch:b.obb", "patch:c.obb"}, false},
		{"omit", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb||patch:c.obb", true, []string{"main:a.obb", "", "patch:c.obb"}, false},
		{"multipleOmit", []string{"w.apk", "x.apk", "y.apk", "z.apk"}, "main:a.obb|||patch:c.obb", true, []string{"main:a.obb", "", "", "patch:c.obb"}, false},
		{"invalid", []string{"x.apk", "y.apk", "z.apk"}, "main:a.obb", false, []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := GetExpansionFiles(tt.appPaths, tt.expansionFilePathConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetExpansionFiles() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.toUpload {
				t.Errorf("GetExpansionFiles() got = %v, want %v", got, tt.toUpload)
			}
			if !reflect.DeepEqual(got1, tt.entries) {
				t.Errorf("GetExpansionFiles() got1 = %v, want %v", got1, tt.entries)
			}
		})
	}
}
