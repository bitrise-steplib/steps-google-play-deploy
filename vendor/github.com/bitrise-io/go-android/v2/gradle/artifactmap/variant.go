package artifactmap

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ArtifactVariant identifies the build variant an artifact belongs to. Values
// are not a stable contract — an artifact and its mapping just need to compare
// equal; the module keeps same-named variants of different modules apart.
type ArtifactVariant struct {
	// Module is the module directory's basename ("app") — the human name used
	// in labels and, when unambiguous, as the document key.
	Module string
	// ModulePath is the full directory path before "build", the module's real
	// identity: distinct modules can share a basename (brandA/app, brandB/app).
	ModulePath string
	// Variant is the merged Gradle variant name ("demoRelease").
	Variant string
}

// VariantFromPath reports the build variant encoded in a Gradle build-output
// path. It reconciles AGP's split (outputs/apk/demo/release/) and merged
// (outputs/{bundle,mapping}/demoRelease/) layouts, so an artifact and its
// mapping resolve to equal ArtifactVariants.
//
// Only official build/outputs/ paths are recognised — anything else
// (intermediates/ task workdirs, Compose mappings, custom destinations)
// reports ok false and stays unmatched. The scan anchors on "outputs"
// followed by the artifact kind, right-to-left so the marker closest to the
// file wins: a checkout directory named "outputs", or a flavor named
// "apk"/"bundle"/"mapping", cannot hijack parsing.
func VariantFromPath(path string) (variant ArtifactVariant, ok bool) {
	segments := strings.Split(filepath.ToSlash(path), "/")

	for i := len(segments) - 2; i >= 0; i-- {
		if segments[i] != "outputs" {
			continue
		}
		switch segments[i+1] {
		// universal_apk is AGP 7's universal-APK output dir (Category.OUTPUTS);
		// AGP 8 dropped the artifact type. AGP's other bundle-derived APK dirs
		// (extracted_apks, apks_from_bundle) are intermediates, so they never
		// reach here — see fromIntermediates.
		case "apk", "bundle", "mapping", "aar", "universal_apk":
		default:
			// some other outputs child (e.g. logs): keep scanning
			continue
		}

		// the directories between the kind marker and the file name encode
		// the variant; none means the marker is not real (a file named
		// "apk"), keep scanning left
		variantSegments := segments[i+2 : max(i+2, len(segments)-1)]
		if len(variantSegments) == 0 {
			continue
		}

		module, modulePath := moduleFromSegments(segments[:i])
		return ArtifactVariant{Module: module, ModulePath: modulePath, Variant: mergeVariantSegments(variantSegments)}, true
	}

	return ArtifactVariant{}, false
}

// moduleFromSegments returns the module directory's basename and full path
// (everything before "build"), or empty strings when the path has no "build"
// segment.
func moduleFromSegments(segments []string) (module, modulePath string) {
	for i := len(segments) - 1; i >= 1; i-- {
		if segments[i] == "build" {
			return segments[i-1], strings.Join(segments[:i], "/")
		}
	}
	return "", ""
}

// mergeVariantSegments joins variant directory segments into the merged Gradle
// variant name: ["demo", "release"] and ["demoRelease"] both yield
// "demoRelease".
func mergeVariantSegments(segments []string) string {
	var builder strings.Builder
	for i, segment := range segments {
		if i == 0 {
			builder.WriteString(segment)
			continue
		}
		builder.WriteString(capitalizeFirst(segment))
	}
	return builder.String()
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	first, size := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(first)) + s[size:]
}
