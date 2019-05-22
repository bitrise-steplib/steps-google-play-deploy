package main

import (
	"reflect"
	"testing"
)

func Test_parseAppList(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotApps := parseAppList(tt.list); !reflect.DeepEqual(gotApps, tt.wantApps) {
				t.Errorf("parseAppList() = %v, want %v", gotApps, tt.wantApps)
			}
		})
	}
}
