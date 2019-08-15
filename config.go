package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"
)

// Configs stores the step's inputs
type Configs struct {
	JSONKeyPath             stepconf.Secret `env:"service_account_json_key_path,required"`
	PackageName             string          `env:"package_name,required"`
	AppPath                 string          `env:"app_path,required"`
	ExpansionfilePath       string          `env:"expansionfile_path"`
	Track                   string          `env:"track,required"`
	UserFraction            float64         `env:"user_fraction,range]0.0..1.0["`
	WhatsnewsDir            string          `env:"whatsnews_dir"`
	MappingFile             string          `env:"mapping_file"`
	UntrackBlockingVersions bool            `env:"untrack_blocking_versions,opt[true,false]"`

	// Deprecated
	ApkPath string `env:"apk_path"`
}

// validate validates the Configs.
func (c Configs) validate() error {
	if err := c.validateJSONKeyPath(); err != nil {
		return err
	}

	if err := c.validateWhatsnewsDir(); err != nil {
		return err
	}

	if err := c.validateMappingFile(); err != nil {
		return err
	}

	return c.validateApps()
}

// validateJSONKeyPath validates if service_account_json_key_path input value exists if defined and has file:// URL scheme.
func (c Configs) validateJSONKeyPath() error {
	if !strings.HasPrefix(string(c.JSONKeyPath), "file://") {
		return nil
	}

	pth := strings.TrimPrefix(string(c.JSONKeyPath), "file://")
	if exist, err := pathutil.IsPathExists(pth); err != nil {
		return fmt.Errorf("failed to check if json key path exist at: %s, error: %s", pth, err)
	} else if !exist {
		return errors.New("json key path not exist at: " + pth)
	}
	return nil
}

// validateWhatsnewsDir validates if whatsnews_dir input value exists if provided.
func (c Configs) validateWhatsnewsDir() error {
	if c.WhatsnewsDir == "" {
		return nil
	}

	if exist, err := pathutil.IsDirExists(c.WhatsnewsDir); err != nil {
		return fmt.Errorf("failed to check if what's new directory exist at: %s, error: %s", c.WhatsnewsDir, err)
	} else if !exist {
		return errors.New("what's new directory not exist at: " + c.WhatsnewsDir)
	}
	return nil
}

// validateMappingFile validates if mapping_file input value exists if provided.
func (c Configs) validateMappingFile() error {
	if c.MappingFile == "" {
		return nil
	}

	if exist, err := pathutil.IsPathExists(c.MappingFile); err != nil {
		return fmt.Errorf("Failed to check if mapping file exist at: %s, error: %s", c.MappingFile, err)
	} else if !exist {
		return errors.New("mapping file not exist at: " + c.MappingFile)
	}
	return nil
}

func parseAPKList(list string) []string {
	return strings.Split(list, "|")
}

func splitElements(list []string, sep string) (s []string) {
	for _, e := range list {
		s = append(s, strings.Split(e, sep)...)
	}
	return
}

func parseAppList(list string) (apps []string) {
	list = strings.TrimSpace(list)
	if len(list) == 0 {
		return nil
	}

	s := []string{list}
	for _, sep := range []string{"\n", `\n`, "|"} {
		s = splitElements(s, sep)
	}

	for _, app := range s {
		app = strings.TrimSpace(app)
		if len(app) > 0 {
			apps = append(apps, app)
		}
	}
	return
}

// appPaths returns the app to deploy, by prefering .aab files.
func (c Configs) appPaths() ([]string, []string) {
	if len(c.ApkPath) > 0 {
		return parseAPKList(c.ApkPath), []string{"step input 'APK file path' (apk_path) is deprecated and will be removed on 20 August 2019, use 'APK or App Bundle file path' (app_path) instead!"}
	}

	var apks, aabs, warnings []string
	for _, pth := range parseAppList(c.AppPath) {
		pth = strings.TrimSpace(pth)
		ext := strings.ToLower(filepath.Ext(pth))
		if ext == ".aab" {
			aabs = append(aabs, pth)
		} else if ext == ".apk" {
			apks = append(apks, pth)
		} else {
			warnings = append(warnings, fmt.Sprintf("unknown app path extension in path: %s, supported extensions: .apk, .aab", pth))
		}
	}

	if len(aabs) > 0 && len(apks) > 0 {
		warnings = append(warnings, fmt.Sprintf("Both .aab and .apk files provided, using the .aab file(s): %s", strings.Join(aabs, ",")))
	}

	if len(aabs) > 1 {
		warnings = append(warnings, fmt.Sprintf("More than 1 .aab files provided, using the first: %s", aabs[0]))
	}

	if len(aabs) > 0 {
		return aabs[:1], warnings
	}

	return apks, warnings
}

// validateApps validates if files provided via apk_path are existing files,
// if apk_path is empty it validates if files provided via app_path input are existing .apk or .aab files.
func (c Configs) validateApps() error {
	apps, warnings := c.appPaths()
	for _, warn := range warnings {
		log.Warnf(warn)
	}

	if len(apps) == 0 {
		return fmt.Errorf("no app provided")
	}

	for _, pth := range apps {
		if exist, err := pathutil.IsPathExists(pth); err != nil {
			return fmt.Errorf("failed to check if app exist at: %s, error: %s", pth, err)
		} else if !exist {
			return errors.New("app not exist at: " + pth)
		}
	}

	return nil
}
