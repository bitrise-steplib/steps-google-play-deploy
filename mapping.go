package main

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
)

// bundleMappingEntry is where an app bundle carries its own R8/ProGuard mapping
// file. AGP's PackageBundleTask passes the mapping to bundletool as the
// `com.android.tools.build.obfuscation` / `proguard.map` metadata file, and
// bundletool stores metadata under BUNDLE-METADATA/<namespace>/<name>.
const bundleMappingEntry = "BUNDLE-METADATA/com.android.tools.build.obfuscation/proguard.map"

// bundleEmbedsMappingFile reports whether the app file is an app bundle that
// carries its own mapping file, in which case Google Play already has it and
// uploading a deobfuscation file for the same version code is redundant.
//
// AGP only embeds the mapping when the build produced one (the bundle task
// skips the metadata file when the obfuscation mapping is absent), and a bundle
// assembled outside AGP may not have it at all, so this looks inside the file
// instead of inferring it from the .aab extension.
func bundleEmbedsMappingFile(appPath string) (bool, error) {
	if !strings.EqualFold(filepath.Ext(appPath), ".aab") {
		return false, nil
	}

	bundle, err := zip.OpenReader(appPath)
	if err != nil {
		return false, fmt.Errorf("failed to open app bundle (%s): %w", appPath, err)
	}
	defer bundle.Close() //nolint:errcheck

	for _, entry := range bundle.File {
		if entry.Name == bundleMappingEntry {
			return true, nil
		}
	}

	return false, nil
}
