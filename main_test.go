package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bitrise-io/go-utils/pathutil"
	"github.com/stretchr/testify/require"
)

func TestParseURI(t *testing.T) {

	t.Log("parseURI - file://../../../../../../Downloads/key.json")
	{
		keyPth, isRemote, err := parseURI("file://../../../../../../Downloads/key.json")
		require.NoError(t, err)

		require.Equal(t, "../../../../../../Downloads/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - file://./")
	{
		keyPth, isRemote, err := parseURI("file://./testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "./testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - file:///")
	{
		keyPth, isRemote, err := parseURI("file:///testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "/testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - http://")
	{
		keyPth, isRemote, err := parseURI("http://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "http://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("parseURI - https://")
	{
		keyPth, isRemote, err := parseURI("https://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "https://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("parseURI - ./")
	{
		keyPth, isRemote, err := parseURI("./user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "./user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("parseURI - /")
	{
		keyPth, isRemote, err := parseURI("/user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "/user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}
}

func createDirStruct(pths []string) error {
	for _, pth := range pths {
		dir := filepath.Dir(pth)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if _, err := os.Create(pth); err != nil {
			return err
		}
	}
	return nil
}

func TestConfigs_validateAndSelectApp(t *testing.T) {
	tmpDir, err := pathutil.NormalizedOSTempDirPath("google-play-deploy")
	if err != nil {
		fmt.Printf("failed to create tmp dir: %s", err)
		os.Exit(1)
	}

	pths := []string{
		filepath.Join(tmpDir, "test.apk"),
		filepath.Join(tmpDir, "test1.apk"),
		filepath.Join(tmpDir, "test.aab"),
		filepath.Join(tmpDir, "test1.aab"),
	}
	if err := createDirStruct(pths); err != nil {
		t.Errorf("failed to create directory struct: %s", err)
		return
	}

	tests := []struct {
		name    string
		ApkPath string
		AppPath string

		apks     []string
		aab      string
		warnings []string
		wantErr  bool
	}{
		{
			name:     "aab path provided via app_path input",
			ApkPath:  "",
			AppPath:  filepath.Join(tmpDir, "test.aab"),
			apks:     nil,
			aab:      filepath.Join(tmpDir, "test.aab"),
			warnings: nil,
			wantErr:  false,
		},
		{
			name:     "multiple aab path provided via app_path input - first aab used",
			ApkPath:  "",
			AppPath:  filepath.Join(tmpDir, "test.aab") + " \n  " + filepath.Join(tmpDir, "test1.aab"),
			apks:     nil,
			aab:      filepath.Join(tmpDir, "test.aab"),
			warnings: []string{"multiple AAB (" + filepath.Join(tmpDir, "test.aab") + "," + filepath.Join(tmpDir, "test1.aab") + ") provided for Google Play deploy, using first: " + filepath.Join(tmpDir, "test.aab")},
			wantErr:  false,
		},
		{
			name:     "apk path provided via app_path input",
			ApkPath:  "",
			AppPath:  filepath.Join(tmpDir, "test.apk"),
			apks:     []string{filepath.Join(tmpDir, "test.apk")},
			aab:      "",
			warnings: nil,
			wantErr:  false,
		},
		{
			name:     "multiple apk path provided via app_path input",
			ApkPath:  "",
			AppPath:  filepath.Join(tmpDir, "test.apk") + "\n" + filepath.Join(tmpDir, "test1.apk"),
			apks:     []string{filepath.Join(tmpDir, "test.apk"), filepath.Join(tmpDir, "test1.apk")},
			aab:      "",
			warnings: nil,
			wantErr:  false,
		},
		{
			name:    "both apk and aab path provided via app_path input - aab is preferred",
			ApkPath: "",
			AppPath: filepath.Join(tmpDir, "test.apk") + "\n" + filepath.Join(tmpDir, "test.aab"),
			apks:    nil,
			aab:     filepath.Join(tmpDir, "test.aab"),
			warnings: []string{
				"both APK (" + filepath.Join(tmpDir, "test.apk") + ") and AAB (" + filepath.Join(tmpDir, "test.aab") + ") files provided for Google Play deploy, using AAB file(s)",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Configs{
				ApkPath: tt.ApkPath,
				AppPath: tt.AppPath,
			}
			got, err := c.validateAndSelectApp()
			if (err != nil) != tt.wantErr {
				t.Errorf("Configs.validateAndSelectApp() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if c.AAB != tt.aab {
				t.Errorf("Configs.aab = %v, want %v", c.AAB, tt.aab)
			}
			if !reflect.DeepEqual(c.APKs, tt.apks) {
				t.Errorf("Configs.apks = %v, want %v", c.APKs, tt.apks)
			}
			if !reflect.DeepEqual(got, tt.warnings) {
				t.Errorf("Configs.validateAndSelectApp() = %v, want %v", got, tt.warnings)
			}
		})
	}
}

func TestConfigs_validateAndSelectApp_deprecation(t *testing.T) {
	tmpDir, err := pathutil.NormalizedOSTempDirPath("google-play-deploy")
	if err != nil {
		fmt.Printf("failed to create tmp dir: %s", err)
		os.Exit(1)
	}

	pths := []string{
		filepath.Join(tmpDir, "test.apk"),
		filepath.Join(tmpDir, "test1.apk"),
		filepath.Join(tmpDir, "test.aab"),
		filepath.Join(tmpDir, "test1.aab"),
	}
	if err := createDirStruct(pths); err != nil {
		t.Errorf("failed to create directory struct: %s", err)
		return
	}

	tests := []struct {
		name    string
		ApkPath string
		AppPath string

		apks     []string
		aab      string
		warnings []string
		wantErr  bool
	}{
		{
			name:     "no app provided",
			ApkPath:  "",
			AppPath:  "",
			apks:     nil,
			aab:      "",
			warnings: nil,
			wantErr:  true,
		},
		{
			name:    "apk path provided via deprecated apk_path input",
			ApkPath: filepath.Join(tmpDir, "test.apk"),
			AppPath: "",
			apks:    []string{filepath.Join(tmpDir, "test.apk")},
			aab:     "",
			warnings: []string{
				"step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!",
				"no app path provided via step input 'APK or App Bundle file path' (app_path), using deprecated step input 'APK file path' (apk_path)",
			},
			wantErr: false,
		},
		{
			name:    "multiple apk path provided via deprecated apk_path input",
			ApkPath: filepath.Join(tmpDir, "test.apk") + "|" + filepath.Join(tmpDir, "test1.apk"),
			AppPath: "",
			apks:    []string{filepath.Join(tmpDir, "test.apk"), filepath.Join(tmpDir, "test1.apk")},
			aab:     "",
			warnings: []string{
				"step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!",
				"no app path provided via step input 'APK or App Bundle file path' (app_path), using deprecated step input 'APK file path' (apk_path)",
			},
			wantErr: false,
		},
		{
			name:    "aab path provided via deprecated apk_path input - handled as apk file",
			ApkPath: filepath.Join(tmpDir, "test.aab"),
			AppPath: "",
			apks:    []string{filepath.Join(tmpDir, "test.aab")},
			aab:     "",
			warnings: []string{
				"step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!",
				"no app path provided via step input 'APK or App Bundle file path' (app_path), using deprecated step input 'APK file path' (apk_path)",
			},
			wantErr: false,
		},
		{
			name:    "multiple aab path provided via deprecated apk_path input - handled as apk files",
			ApkPath: filepath.Join(tmpDir, "test.aab") + "|" + filepath.Join(tmpDir, "test1.aab"),
			AppPath: "",
			apks:    []string{filepath.Join(tmpDir, "test.aab"), filepath.Join(tmpDir, "test1.aab")},
			aab:     "",
			warnings: []string{
				"step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!",
				"no app path provided via step input 'APK or App Bundle file path' (app_path), using deprecated step input 'APK file path' (apk_path)",
			},
			wantErr: false,
		},
		{
			name:    "apk provided via deprecated apk_path input and new app_path input",
			ApkPath: filepath.Join(tmpDir, "test.apk"),
			AppPath: filepath.Join(tmpDir, "test1.apk"),
			apks:    []string{filepath.Join(tmpDir, "test1.apk")},
			aab:     "",
			warnings: []string{
				"step input 'APK file path' (apk_path) is deprecated and will be removed soon, use 'APK or App Bundle file path' (app_path) instead!",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Configs{
				ApkPath: tt.ApkPath,
				AppPath: tt.AppPath,
			}
			got, err := c.validateAndSelectApp()
			if (err != nil) != tt.wantErr {
				t.Errorf("Configs.validateAndSelectApp() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if c.AAB != tt.aab {
				t.Errorf("Configs.aab = %v, want %v", c.AAB, tt.aab)
			}
			if !reflect.DeepEqual(c.APKs, tt.apks) {
				t.Errorf("Configs.apks = %v, want %v", c.APKs, tt.apks)
			}
			if !reflect.DeepEqual(got, tt.warnings) {
				t.Errorf("Configs.validateAndSelectApp() = %v, want %v", got, tt.warnings)
			}
		})
	}
}

func Test_parseAppList(t *testing.T) {
	tests := []struct {
		name     string
		appList  string
		wantApks []string
		wantAabs []string
		wantErr  bool
	}{
		{
			name:     "",
			appList:  "test.apk\ntest1.apk\ntest2.aab",
			wantApks: []string{"test.apk", "test1.apk"},
			wantAabs: []string{"test2.aab"},
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotApks, gotAabs, err := parseAppList(tt.appList)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAppList() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotApks, tt.wantApks) {
				t.Errorf("ParseAppList() gotApks = %v, want %v", gotApks, tt.wantApks)
			}
			if !reflect.DeepEqual(gotAabs, tt.wantAabs) {
				t.Errorf("ParseAppList() gotAabs = %v, want %v", gotAabs, tt.wantAabs)
			}
		})
	}
}
