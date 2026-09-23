package gitdiff

import (
	"path/filepath"
	"regexp"
	"strconv"
)

// LineRange is an inclusive [Start, End] range of 1-based line numbers in
// the new (current) version of a file.
type LineRange struct {
	Start, End int
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ChangedHunks returns the line ranges touched in the new (current)
// version of absPath, one per unified-diff hunk - for a caller that needs
// to know not just *that* a file changed (ChangedFiles) but *where*
// within it, e.g. to test whether every change falls inside a single
// function body.
//
// ok is false whenever the result can't be trusted for that kind of
// precise mapping, rather than trying to report a partial or approximate
// answer:
//   - A hunk that's a pure deletion (removes lines without adding any, so
//     it has no corresponding new-file line range at all) makes the whole
//     result unusable for mapping onto the new file's contents.
//   - No hunks at all usually means an untracked new file (`git diff`
//     doesn't cover those) or a binary file - both cases where there's
//     nothing meaningful to report, but silently returning an empty
//     result could be mistaken by a caller for "confirmed no changes"
//     rather than "unknown".
//
// base has the same meaning as in ChangedFiles: empty compares the
// working tree against HEAD, non-empty does a three-dot diff against
// base's merge-base with HEAD.
func ChangedHunks(repoRoot, base, absPath string) (ranges []LineRange, ok bool, err error) {
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return nil, false, err
	}
	rel = filepath.ToSlash(rel)

	var args []string
	if base == "" {
		args = []string{"diff", "-U0", "HEAD", "--", rel}
	} else {
		if err := ensureMergeBaseHistory(repoRoot, base); err != nil {
			return nil, false, err
		}
		args = []string{"diff", "-U0", base + "...HEAD", "--", rel}
	}

	lines, err := runGit(repoRoot, args...)
	if err != nil {
		return nil, false, err
	}

	for _, line := range lines {
		m := hunkHeaderRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, false, nil
		}
		count := 1
		if m[2] != "" {
			count, err = strconv.Atoi(m[2])
			if err != nil {
				return nil, false, nil
			}
		}
		if count == 0 {
			return nil, false, nil // pure deletion - see the doc comment.
		}
		ranges = append(ranges, LineRange{Start: start, End: start + count - 1})
	}
	if len(ranges) == 0 {
		return nil, false, nil // untracked/binary/no-op - see the doc comment.
	}
	return ranges, true, nil
}
