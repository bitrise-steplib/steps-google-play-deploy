package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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

// appPaths returns the app to deploy, by preferring .aab files.
func (c Configs) appPaths() ([]string, []string) {
	var apks, aabs, warnings []string
	for _, pth := range c.parseInputList(c.AppPath) {
		pth = strings.TrimSpace(pth)
		ext := strings.ToLower(filepath.Ext(pth))
		switch ext {
		case ".aab":
			aabs = append(aabs, pth)
		case ".apk":
			apks = append(apks, pth)
		default:
			warnings = append(warnings, fmt.Sprintf("unknown app path extension in path: %s, supported extensions: .apk, .aab", pth))
		}
	}

	if len(aabs) > 0 && len(apks) > 0 {
		warnings = append(warnings, fmt.Sprintf("Both .aab and .apk files provided, using the .aab file(s): %s", strings.Join(aabs, ",")))
	}

	if len(aabs) > 0 {
		return aabs, warnings
	}

	return apks, warnings
}

// mappingPaths returns the candidate mapping files.
//
// This is a pool of candidates, not a list positionally matched to app_path: each
// artifact is paired with its own mapping by content, via the pg_map_id that R8
// writes into both the artifact and the mapping file. The order is therefore
// irrelevant, and so is the count.
//
// Uses parseInputList so newline-separated values work as step.yml documents.
// Splitting on "|" alone silently produced a single element containing newlines,
// which then failed to open.
func (c Configs) mappingPaths() []string {
	return c.parseInputList(c.MappingFile)
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
