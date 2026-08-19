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
		// reach here — see fromIntermediates. AARs have no variant directories
		// at all — see AARVariantFromPath.
		case "apk", "bundle", "mapping", "universal_apk":
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

// AARVariantFromPath reports the build variant of a library archive under
// build/outputs/aar/. AGP puts every variant's archive in that one directory
// and encodes the variant in the file name instead:
//
//	mylib/build/outputs/aar/mylib-free-release.aar   -> mylib, freeRelease
//	mylib/build/outputs/aar/mylib-debug.aar          -> mylib, debug
//
// The name is <archivesName>-<flavors...>-<buildType>.aar, and archivesName
// defaults to the module directory's name. Only that default is decoded, and
// every remaining segment must look like a Gradle flavor or build type (a
// valid identifier — they become generated accessors, so "1.0" cannot be one).
// Anything else leaves no way to tell where the name ends and the variant
// begins, so those archives report ok false and stay visible under Unmatched
// rather than being filed under a made-up variant. A customised archivesName
// that does parse as identifiers ("mylib-v2") is indistinguishable from a
// flavor and will be read as one.
//
// A self-minifying library writes its mapping to the standard
// outputs/mapping/<variant>/ tree, so a decoded archive pairs with its own
// mapping file through the usual VariantFromPath.
func AARVariantFromPath(path string) (variant ArtifactVariant, ok bool) {
	slashed := filepath.ToSlash(path)
	segments := strings.Split(slashed, "/")

	for i := len(segments) - 2; i >= 0; i-- {
		if segments[i] != "outputs" || segments[i+1] != "aar" {
			continue
		}
		// the archive sits directly in outputs/aar/, nothing in between
		if i+2 != len(segments)-1 {
			continue
		}

		module, modulePath := moduleFromSegments(segments[:i])
		name := strings.TrimSuffix(segments[len(segments)-1], filepath.Ext(segments[len(segments)-1]))
		rest, matched := strings.CutPrefix(name, module+"-")
		if module == "" || !matched || rest == "" {
			return ArtifactVariant{}, false
		}

		variantSegments := strings.Split(rest, "-")
		for _, segment := range variantSegments {
			if !isGradleName(segment) {
				return ArtifactVariant{}, false
			}
		}

		return ArtifactVariant{
			Module:     module,
			ModulePath: modulePath,
			Variant:    mergeVariantSegments(variantSegments),
		}, true
	}

	return ArtifactVariant{}, false
}

// isGradleName reports whether the segment can be a Gradle flavor or build type
// name: those are used to generate task and accessor names, so they are
// identifiers — no leading digit, no dots.
func isGradleName(segment string) bool {
	if segment == "" {
		return false
	}
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
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
