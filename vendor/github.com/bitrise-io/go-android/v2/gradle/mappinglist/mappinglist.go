// Package mappinglist encodes and decodes the positional list of mapping-file
// paths that the Android build steps export as BITRISE_MAPPING_PATH_LIST and
// the Google Play Deploy step consumes.
//
// The list is positional: entry N is the mapping file for app artifact N in the
// matching app list (BITRISE_AAB_PATH_LIST / BITRISE_APK_PATH_LIST). An empty
// entry means "app artifact N has no mapping file"; empty entries are
// significant and are preserved so they never shift the alignment of the
// entries that follow them (e.g. "a.txt||c.txt" is three entries, the middle
// one empty).
//
// Encode is used by the producing steps; Decode by the consuming step. Keeping
// both here guarantees the two sides agree on the format. The package has no
// dependencies beyond the standard library, so a consumer that only needs the
// codec does not pull in the rest of the gradle package.
package mappinglist

import "strings"

// separator joins the list. The producing steps always emit this; Decode also
// tolerates newline separators for hand-authored input.
const separator = "|"

// Encode joins mapping paths into the list format, keeping empty entries as
// empty fields (e.g. []string{"a", "", "c"} -> "a||c"). The producing steps
// only emit a list that has at least one real path, so the ambiguous all-empty
// case (e.g. Encode([]string{""}) == "") is never exported.
func Encode(paths []string) string {
	return strings.Join(paths, separator)
}

// Decode parses the list format back into a positional slice, PRESERVING empty
// entries so index alignment with the app list is not lost. It tolerates the
// pipe separator plus newline and literal `\n` separators (the value may be set
// by hand in a step input), trims whitespace around the whole value and around
// each entry, and returns nil for an empty value.
func Decode(list string) []string {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil
	}

	fields := []string{list}
	for _, sep := range []string{"\n", `\n`, separator} {
		fields = splitEach(fields, sep)
	}

	paths := make([]string, len(fields))
	for i, field := range fields {
		paths[i] = strings.TrimSpace(field)
	}
	return paths
}

func splitEach(in []string, sep string) []string {
	var out []string
	for _, element := range in {
		out = append(out, strings.Split(element, sep)...)
	}
	return out
}
