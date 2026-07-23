package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-android/v2/gradle/mappinglist"
	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/pathutil"
	"github.com/bitrise-io/go-utils/v2/log"
)

// Configs stores the step's inputs
type Configs struct {
	JSONKeyPath                  stepconf.Secret `env:"service_account_json_key_path,required"`
	PackageName                  string          `env:"package_name,required"`
	AppPath                      string          `env:"app_path,required"`
	ExpansionfilePath            string          `env:"expansionfile_path"`
	Track                        string          `env:"track"`
	UserFraction                 float64         `env:"user_fraction,range]0.0..1.0["`
	UpdatePriority               int             `env:"update_priority,range[0..5]"`
	WhatsnewsDir                 string          `env:"whatsnews_dir"`
	MappingFile                  string          `env:"mapping_file"`
	ReleaseName                  string          `env:"release_name"`
	Status                       string          `env:"status"`
	RetryWithoutSendingToReview  bool            `env:"retry_without_sending_to_review,opt[true,false]"`
	AckBundleInstallationWarning bool            `env:"ack_bundle_installation_warning,opt[true,false]"`
	DryRun                       bool            `env:"dry_run,opt[true,false]"`
	IsDebugLog                   bool            `env:"verbose_log,opt[true,false]"`
	Logger                       log.Logger
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

	c.Logger.Infof("Using what's new data from: %v", c.WhatsnewsDir)
	return nil
}

// validateMappingFile validates if mapping_file input value exists if provided.
func (c Configs) validateMappingFile() error {
	if c.MappingFile == "" {
		return nil
	}

	for _, path := range c.parseInputList(c.MappingFile) {
		if exist, err := pathutil.IsPathExists(path); err != nil {
			return fmt.Errorf("failed to check if mapping file exist at: %s, error: %s", path, err)
		} else if !exist {
			return errors.New("mapping file doesn't exist at: " + path)
		}

		c.Logger.Infof("Using mapping file from: %v", path)
	}
	return nil
}

func splitElements(list []string, sep string) (s []string) {
	for _, e := range list {
		s = append(s, strings.Split(e, sep)...)
	}
	return
}

func (c Configs) parseInputList(list string) (elements []string) {
	c.Logger.Debugf("Parsing list input: '%v'", list)
	list = strings.TrimSpace(list)
	if len(list) == 0 {
		return nil
	}

	s := []string{list}
	for _, sep := range []string{"\n", `\n`, "|"} {
		s = splitElements(s, sep)
	}

	for _, element := range s {
		element = strings.TrimSpace(element)
		if len(element) > 0 {
			elements = append(elements, element)
			c.Logger.Debugf("Found element: %v", element)
		}
	}
	return
}

// appArtifact is an app file to deploy together with the mapping file that
// belongs to it (empty when the app has no mapping file).
type appArtifact struct {
	path        string
	mappingPath string
}

// appsToDeploy pairs each app in app_path with the mapping file at the same
// position in mapping_file, then selects the artifacts to deploy, preferring
// .aab over .apk. Pairing is done BEFORE the .aab/.apk selection so that
// dropping an .apk also drops its mapping file and never shifts the alignment
// of the remaining app<->mapping pairs.
func (c Configs) appsToDeploy() ([]appArtifact, []string) {
	apps := c.parseInputList(c.AppPath)
	mappings := mappinglist.Decode(c.MappingFile)

	var apks, aabs []appArtifact
	var warnings []string
	for i, pth := range apps {
		pth = strings.TrimSpace(pth)
		mapping := ""
		if i < len(mappings) {
			mapping = mappings[i]
		}
		artifact := appArtifact{path: pth, mappingPath: mapping}

		switch strings.ToLower(filepath.Ext(pth)) {
		case ".aab":
			aabs = append(aabs, artifact)
		case ".apk":
			apks = append(apks, artifact)
		default:
			warnings = append(warnings, fmt.Sprintf("unknown app path extension in path: %s, supported extensions: .apk, .aab", pth))
		}
	}

	if len(mappings) > len(apps) {
		warnings = append(warnings, fmt.Sprintf("More mapping files (%d) provided than app files (%d); the extra mapping files are ignored. Check that the mapping_file list is aligned with the app_path list.", len(mappings), len(apps)))
	}

	if len(aabs) > 0 && len(apks) > 0 {
		var aabPaths []string
		for _, a := range aabs {
			aabPaths = append(aabPaths, a.path)
		}
		warnings = append(warnings, fmt.Sprintf("Both .aab and .apk files provided, using the .aab file(s): %s", strings.Join(aabPaths, ",")))
	}

	if len(aabs) > 0 {
		return aabs, warnings
	}

	return apks, warnings
}

// appPaths returns the paths of the apps to deploy, by preferring .aab files.
func (c Configs) appPaths() ([]string, []string) {
	apps, warnings := c.appsToDeploy()
	var paths []string
	for _, a := range apps {
		paths = append(paths, a.path)
	}
	return paths, warnings
}

// validateApps validates if files provided via app_path are existing files,
// if app_path is empty it validates if files provided via app_path input are existing .apk or .aab files.
func (c Configs) validateApps() error {
	apps, warnings := c.appPaths()
	for _, warn := range warnings {
		c.Logger.Warnf(warn)
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
		c.Logger.Infof("Using app from: %v", pth)
	}

	return nil
}

// expansionFiles gets the expansion files from the received configuration. Returns true and the entries (type and
// path) of them when any found, false or error otherwise.
func (c Configs) expansionFiles(appPaths []string) ([]string, error) {
	// "main:/file/path/1.obb|patch:/file/path/2.obb"
	var expansionFileEntries = []string{}
	if strings.TrimSpace(c.ExpansionfilePath) != "" {
		expansionFileEntries = strings.Split(c.ExpansionfilePath, "|")

		if len(appPaths) != len(expansionFileEntries) {
			return []string{}, fmt.Errorf("mismatching number of APKs(%d) and Expansionfiles(%d)", len(appPaths), len(expansionFileEntries))
		}

		c.Logger.Infof("Found %v expansion file(s) to upload.", len(expansionFileEntries))
		for i, expansionFile := range expansionFileEntries {
			c.Logger.Debugf("%v - %v", i+1, expansionFile)
		}
	}
	return expansionFileEntries, nil
}
