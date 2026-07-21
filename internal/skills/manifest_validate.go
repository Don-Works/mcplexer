package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Sentinel errors returned by Validate. Callers should match with errors.Is
// to give the user actionable feedback (file lints, install rejection, etc).
var (
	// ErrInvalidManifest is the umbrella error returned when one or more
	// validation checks fail. The wrapped error chain identifies which.
	ErrInvalidManifest = errors.New("invalid manifest")

	// ErrMissingField indicates a required field was empty or absent.
	ErrMissingField = errors.New("missing required field")

	// ErrInvalidVersion indicates a version string is not valid semver
	// (or a version range is not a valid range expression).
	ErrInvalidVersion = errors.New("invalid version")

	// ErrInvalidName indicates the skill name does not match the allowed
	// character set / shape.
	ErrInvalidName = errors.New("invalid name")

	// ErrUnsupportedManifestVersion indicates manifest_version is unknown
	// to this build of mcplexer.
	ErrUnsupportedManifestVersion = errors.New("unsupported manifest version")

	// ErrInvalidCapability indicates a capability declaration is malformed
	// (e.g. unknown filesystem mode, conflicting fields).
	ErrInvalidCapability = errors.New("invalid capability")

	// ErrInvalidSignatureFormat indicates the signature field is non-empty
	// but does not parse as a minisign signature blob. See ADR 0002.
	ErrInvalidSignatureFormat = errors.New("invalid signature format")
)

// Strict semver regex: MAJOR.MINOR.PATCH with optional -prerelease and +build.
// Source: https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
var semverRE = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)` +
		`(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
		`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`,
)

// Skill names: lowercase alnum + hyphens, optional one-segment scope prefix
// (e.g. "blog-post", "@example/blog-post"). Must start and end with alnum.
var nameRE = regexp.MustCompile(
	`^(?:@[a-z0-9][a-z0-9-]*[a-z0-9]/)?[a-z0-9][a-z0-9-]*[a-z0-9]$`,
)

// Range operators we accept in version-range strings. We do not yet evaluate
// ranges; we only validate that each comparator parses to a clean shape.
var rangeOpRE = regexp.MustCompile(`^([=!<>~^]=?|<=|>=)?\s*(.+)$`)

// Validate checks a Manifest for structural and semantic errors. It returns
// nil for a valid manifest, or an error wrapping one or more sentinels.
// Errors are joined with errors.Join so callers can errors.Is against any
// individual sentinel.
func Validate(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("%w: nil manifest", ErrInvalidManifest)
	}
	var errs []error
	errs = append(errs, validateIdentity(m)...)
	errs = append(errs, validateDependencies(m)...)
	errs = append(errs, validateCapabilities(&m.Capabilities)...)
	if err := ValidateExtra(m.ManifestExtra); err != nil {
		errs = append(errs, err)
	}
	if m.MCPlexerMinVersion != "" && !semverRE.MatchString(m.MCPlexerMinVersion) {
		errs = append(errs,
			fmt.Errorf("%w: mcplexer_min_version %q is not valid semver",
				ErrInvalidVersion, m.MCPlexerMinVersion))
	}
	if m.Signature != "" && !IsValidSignatureBlob(m.Signature) {
		errs = append(errs,
			fmt.Errorf("%w: signature is not a valid minisign blob",
				ErrInvalidSignatureFormat))
	}
	errs = filterNil(errs)
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidManifest, errors.Join(errs...))
}

// validateIdentity checks the name/version/manifest_version/description trio.
func validateIdentity(m *Manifest) []error {
	var errs []error
	if m.ManifestVersion == 0 {
		errs = append(errs, fmt.Errorf("%w: manifest_version", ErrMissingField))
	} else if m.ManifestVersion != ManifestVersion {
		errs = append(errs,
			fmt.Errorf("%w: manifest_version %d (this build supports %d)",
				ErrUnsupportedManifestVersion, m.ManifestVersion, ManifestVersion))
	}
	if m.Name == "" {
		errs = append(errs, fmt.Errorf("%w: name", ErrMissingField))
	} else if !nameRE.MatchString(m.Name) {
		errs = append(errs,
			fmt.Errorf("%w: name %q must be lowercase alnum + hyphens (optional @scope/)",
				ErrInvalidName, m.Name))
	}
	if m.Version == "" {
		errs = append(errs, fmt.Errorf("%w: version", ErrMissingField))
	} else if !semverRE.MatchString(m.Version) {
		errs = append(errs,
			fmt.Errorf("%w: version %q is not valid semver", ErrInvalidVersion, m.Version))
	}
	if m.Description == "" {
		errs = append(errs, fmt.Errorf("%w: description", ErrMissingField))
	}
	return errs
}

// validateDependencies checks each declared dependency entry.
func validateDependencies(m *Manifest) []error {
	var errs []error
	for depName, dep := range m.Dependencies {
		if !nameRE.MatchString(depName) {
			errs = append(errs,
				fmt.Errorf("%w: dependency name %q", ErrInvalidName, depName))
		}
		if dep.Version == "" {
			errs = append(errs,
				fmt.Errorf("%w: dependencies[%q].version", ErrMissingField, depName))
			continue
		}
		if err := validateVersionRange(dep.Version); err != nil {
			errs = append(errs,
				fmt.Errorf("%w: dependencies[%q]: %w", ErrInvalidVersion, depName, err))
		}
	}
	return errs
}

// validateCapabilities walks the Capabilities struct and reports problems.
func validateCapabilities(c *Capabilities) []error {
	var errs []error
	for i, srv := range c.MCPServers {
		if srv.Name == "" {
			errs = append(errs,
				fmt.Errorf("%w: mcp_servers[%d].name", ErrMissingField, i))
		}
		if srv.Version != "" {
			if err := validateVersionRange(srv.Version); err != nil {
				errs = append(errs,
					fmt.Errorf("%w: mcp_servers[%d]: %w", ErrInvalidVersion, i, err))
			}
		}
	}
	errs = append(errs, validateFilesystem(&c.Filesystem)...)
	if !c.Network.Enabled && len(c.Network.AllowedHosts) > 0 {
		errs = append(errs,
			fmt.Errorf("%w: network.allowed_hosts set while network.enabled is false",
				ErrInvalidCapability))
	}
	return errs
}

// validateFilesystem checks filesystem mode + paths consistency.
func validateFilesystem(fs *FilesystemCapability) []error {
	var errs []error
	switch fs.Mode {
	case "", FilesystemModeNone:
		if len(fs.Paths) > 0 {
			errs = append(errs,
				fmt.Errorf("%w: filesystem.paths set while mode is none",
					ErrInvalidCapability))
		}
	case FilesystemModeReadOnly, FilesystemModeReadWrite:
		if len(fs.Paths) == 0 {
			errs = append(errs,
				fmt.Errorf("%w: filesystem.mode %q requires at least one path",
					ErrInvalidCapability, fs.Mode))
		}
	default:
		errs = append(errs,
			fmt.Errorf("%w: filesystem.mode %q (want one of: none, read_only, read_write)",
				ErrInvalidCapability, fs.Mode))
	}
	return errs
}

// validateVersionRange checks that a comma-separated list of comparators
// (e.g. ">=1.2.0, <2.0.0") parses cleanly. We do not yet evaluate the
// resulting range; a downstream package may pull in a semver lib.
func validateVersionRange(rng string) error {
	rng = strings.TrimSpace(rng)
	if rng == "" {
		return fmt.Errorf("%w: empty range", ErrInvalidVersion)
	}
	parts := strings.Split(rng, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		m := rangeOpRE.FindStringSubmatch(p)
		if m == nil {
			return fmt.Errorf("%w: comparator %q", ErrInvalidVersion, p)
		}
		ver := strings.TrimSpace(m[2])
		// Accept x-ranges and partial versions: 1, 1.2, 1.2.x, 1.x.x
		if isXRange(ver) {
			continue
		}
		if !semverRE.MatchString(ver) {
			return fmt.Errorf("%w: comparator %q has invalid version %q",
				ErrInvalidVersion, p, ver)
		}
	}
	return nil
}

// isXRange reports whether v is a permissible partial / wildcard version.
// Examples: "1", "1.2", "1.2.x", "1.x.x", "*".
func isXRange(v string) bool {
	if v == "*" || v == "" {
		return true
	}
	segs := strings.Split(v, ".")
	if len(segs) > 3 {
		return false
	}
	for _, s := range segs {
		if s == "x" || s == "X" || s == "*" {
			continue
		}
		// numeric segment is fine
		for _, r := range s {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// filterNil drops nil entries from a slice of errors.
func filterNil(in []error) []error {
	out := in[:0]
	for _, e := range in {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}
