// Package version compares tess versions, so plugins can state which tess releases they
// work with.
//
// Versions are semantic versions, MAJOR.MINOR.PATCH with an optional pre-release, and an
// optional leading "v". A development build ("dev") counts as newer than every release.
package version

import (
	"cmp"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

// Dev is the version of a build made without a release version.
const Dev = "dev"

// Describe returns v for display. A development build gains its commit, when the Go build
// recorded one.
func Describe(v string) string {
	if v != Dev {
		return v
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return v
	}
	return v + " (" + rev[:min(12, len(rev))] + dirty + ")"
}

type semver struct {
	nums [3]int
	pre  string
}

func parse(v string) (semver, error) {
	var s semver
	core := strings.TrimPrefix(v, "v")
	core, _, _ = strings.Cut(core, "+") // build metadata does not order
	core, s.pre, _ = strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return s, fmt.Errorf("%q is not a version like 1.2.3", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return s, fmt.Errorf("%q is not a version like 1.2.3", v)
		}
		s.nums[i] = n
	}
	return s, nil
}

// Valid reports whether v parses.
func Valid(v string) bool {
	_, err := parse(v)
	return err == nil
}

// Compare orders two versions like cmp.Compare. Dev is the newest.
func Compare(a, b string) (int, error) {
	if a == Dev || b == Dev {
		switch {
		case a == b:
			return 0, nil
		case a == Dev:
			return 1, nil
		}
		return -1, nil
	}
	x, err := parse(a)
	if err != nil {
		return 0, err
	}
	y, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range x.nums {
		if c := cmp.Compare(x.nums[i], y.nums[i]); c != 0 {
			return c, nil
		}
	}
	return comparePre(x.pre, y.pre), nil
}

// comparePre follows semver: a release is newer than its pre-releases, and pre-release
// identifiers compare numerically when both are numbers.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aerr == nil && berr == nil:
			c = cmp.Compare(an, bn)
		case aerr == nil:
			c = -1
		case berr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// Supports checks that tess version v lies within [min, max], where an empty bound is open.
// A development build passes a minimum but is checked against a maximum like a release
// newer than all others, so an upper bound is noticed during development too.
func Supports(v, min, max string) error {
	if min != "" {
		if !Valid(min) {
			return fmt.Errorf("minimum tess version %q is not a version like 1.2.3", min)
		}
		if c, err := Compare(v, min); err != nil {
			return err
		} else if c < 0 {
			return fmt.Errorf("needs tess %s or newer, this is tess %s", min, v)
		}
	}
	if max != "" {
		if !Valid(max) {
			return fmt.Errorf("maximum tess version %q is not a version like 1.2.3", max)
		}
		if c, err := Compare(v, max); err != nil {
			return err
		} else if c > 0 {
			return fmt.Errorf("works only up to tess %s, this is tess %s", max, v)
		}
	}
	return nil
}
