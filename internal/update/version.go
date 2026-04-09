package update

import (
	"fmt"
	"strings"
)

// Version represents a semantic version (major.minor.patch).
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// ParseVersion parses "v1.2.3" or "1.2.3" into a Version.
// Returns error for non-semver strings (commit hashes, "dev", etc.).
func ParseVersion(s string) (Version, error) {
	raw := s
	s = strings.TrimPrefix(s, "v")
	var v Version
	v.Raw = raw
	n, err := fmt.Sscanf(s, "%d.%d.%d", &v.Major, &v.Minor, &v.Patch)
	if err != nil || n != 3 {
		return Version{}, fmt.Errorf("not a semver string: %q", raw)
	}
	return v, nil
}

// NewerThan returns true if v is a newer version than other.
func (v Version) NewerThan(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch > other.Patch
}

// String returns the version as "v1.2.3".
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}
