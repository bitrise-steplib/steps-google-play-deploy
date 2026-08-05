package artifactmap

import (
	"path/filepath"
	"strings"
)

// ArtifactVariant identifies the build variant an artifact belongs to: the
// module plus the merged Gradle variant name ("demoRelease"). Its textual form
// is not a stable contract — an app artifact and the mapping.txt built for the
// same variant just need to produce equal values. The module prevents
// same-named variants of different modules from colliding.
type ArtifactVariant struct {
	Module  string
	Variant string
}

// VariantFromPath reports the build variant encoded in a Gradle build-output
// path, reconciling the layouts the Android Gradle Plugin uses: the split
// flavor/buildType shape (outputs/apk/demo/release/, ProGuard-era
// outputs/mapping/demo/release/), the merged shape
// (outputs/{bundle,mapping}/demoRelease/), and a trailing shrinker-task
// subdirectory (intermediates/mapping/demoRelease/minifyDemoReleaseWithR8/).
// Split segments merge into the same name, so an app artifact and its mapping
// resolve to equal ArtifactVariants.
//
// It anchors on the "outputs"/"intermediates" directory followed by the
// artifact kind, scanning right-to-left so the marker closest to the file
// wins — a checkout directory named "outputs", or a flavor named
// "apk"/"bundle"/"mapping", cannot hijack parsing. ok is false when the path
// is not a recognised output or mapping path.
func VariantFromPath(path string) (variant ArtifactVariant, ok bool) {
	segments := strings.Split(filepath.ToSlash(path), "/")

	for i := len(segments) - 2; i >= 0; i-- {
		if segments[i] != "outputs" && segments[i] != "intermediates" {
			continue
		}

		// The directories between the kind marker and the file name encode
		// the variant; none means this is not a real marker (e.g. the file
		// itself is named "apk"), keep scanning left.
		variantSegments := segments[i+2 : max(i+2, len(segments)-1)]
		if len(variantSegments) == 0 {
			continue
		}

		module := moduleFromSegments(segments[:i])
		switch segments[i+1] {
		case "apk", "bundle":
			return ArtifactVariant{Module: module, Variant: mergeVariantSegments(variantSegments)}, true
		case "mapping":
			// drop a trailing shrinker-task dir ("minifyDemoReleaseWithR8")
			if len(variantSegments) > 1 && strings.HasPrefix(variantSegments[len(variantSegments)-1], "minify") {
				variantSegments = variantSegments[:len(variantSegments)-1]
			}
			return ArtifactVariant{Module: module, Variant: mergeVariantSegments(variantSegments)}, true
		}
		// some other outputs child (e.g. logs): keep scanning
	}

	return ArtifactVariant{}, false
}

// moduleFromSegments returns the module directory (the segment right before
// "build"), or "" when the path has no "build" segment.
func moduleFromSegments(segments []string) string {
	for i := len(segments) - 1; i >= 1; i-- {
		if segments[i] == "build" {
			return segments[i-1]
		}
	}
	return ""
}

// mergeVariantSegments joins variant directory segments into the single merged
// Gradle variant name: the first segment as-is, each following one capitalised
// on its first letter, so ["demo", "release"] and ["demoRelease"] both yield
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
	return strings.ToUpper(s[:1]) + s[1:]
}
