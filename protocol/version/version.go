// Package version carries the protocol identity of the shipped game client and
// the rules that decide when recorded traffic, an opcode table, or a struct
// layout may be reused.
//
// Everything in this package is a *declared* fact taken from the shipped
// artifact. See docs/EVIDENCE.md for the source of each value.
package version

import (
	"errors"
	"fmt"
)

// ID identifies one shipped build. Two builds that differ in Build may share a
// version string but never share an opcode numbering.
type ID struct {
	Product  string // product flavor, e.g. "wowf" (World of Warcraft: Forever)
	Branch   string // .build.info Branch column
	Major    int
	Minor    int
	Patch    int
	Build    int
	BuildKey string // .build.info "Build Key" (CASC build config key)
	CDNKey   string // .build.info "CDN Key"
}

// Known builds. Each has its own opcode numbering (protocol/opcodes) and its
// own per-build auth key (world.BuildAuthKey); nothing recorded against one
// is valid for the other without a check.
var (
	// Build70707 is CFBundleVersion 1.60.1.70707, the build everything was
	// first decoded against (artifacts/WoWForever-arm64.decrypted).
	Build70707 = ID{
		Product: "wowf", Branch: "us", Major: 1, Minor: 60, Patch: 1, Build: 70707,
		BuildKey: "7feb61f3c994bcb168526b8db521514a",
		CDNKey:   "aced69cc895b8fbb802f35e442ebe6fc",
	}
	// Build70755 is CFBundleVersion 1.60.1.70755, shipped by Battle.net on
	// 2026-11-03/04 and installed since. It renumbers two opcode groups
	// against 70707 (docs/EVIDENCE.md E31).
	Build70755 = ID{
		Product: "wowf", Branch: "us", Major: 1, Minor: 60, Patch: 1, Build: 70755,
		BuildKey: "9d9888f2d89a2dcc9eacc68a2d7cb3dc",
		CDNKey:   "483baa37d48c06ae7d14c83a591c469e",
	}
)

// Forever returns the identity of the reference target, the installed client:
// /Applications/World of Warcraft/_forever_/World of Warcraft.app,
// CFBundleVersion 1.60.1.70755, product flavor "wowf". Build70707 stays known
// and selectable through ByBuild.
func Forever() ID { return Build70755 }

// Known lists every build this client has tables for, oldest first.
func Known() []ID { return []ID{Build70707, Build70755} }

// ByBuild returns the known identity with that build number.
func ByBuild(build int) (ID, bool) {
	for _, v := range Known() {
		if v.Build == build {
			return v, true
		}
	}
	return ID{}, false
}

// Parse resolves a protocol string ("wowf 1.60.1.70707", as v.String()
// renders it) to a known identity.
func Parse(protocol string) (ID, bool) {
	for _, v := range Known() {
		if v.String() == protocol {
			return v, true
		}
	}
	return ID{}, false
}

// ErrUnknownBuild is returned for a build this client has no tables for.
var ErrUnknownBuild = errors.New("unknown protocol build")

// VersionString renders the marketing version ("1.60.1").
func (v ID) VersionString() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// String renders the full identity ("wowf 1.60.1.70707").
func (v ID) String() string { return fmt.Sprintf("%s %s.%d", v.Product, v.VersionString(), v.Build) }

// ErrBuildMismatch is returned when two identities disagree on the exact build.
var ErrBuildMismatch = errors.New("protocol build mismatch")

// Check reports whether a recorded artifact (opcode table, fixture, capture) is
// usable against v. Only an exact build match is accepted: opcode numbering is
// rebuilt every patch and there is no public stability guarantee (70755 moved
// two groups against 70707 without changing the version string), and v itself
// must be a known build.
func (v ID) Check(want ID) error {
	if v.Product != want.Product || v.Major != want.Major || v.Minor != want.Minor ||
		v.Patch != want.Patch || v.Build != want.Build {
		return fmt.Errorf("%w: have %s, want %s", ErrBuildMismatch, v, want)
	}
	if _, ok := ByBuild(v.Build); !ok || v.Product != Forever().Product {
		return fmt.Errorf("%w: %s", ErrUnknownBuild, v)
	}
	return nil
}

// Compatible reports whether both identities can share a protocol table.
func (v ID) Compatible(other ID) bool { return v.Check(other) == nil }

// All returns every known build, oldest build number first.
func All() []ID { return Known() }

// Lookup returns the identity of a known build.
func Lookup(build int) (ID, bool) { return ByBuild(build) }

// Latest returns the newest known build by build number.
func Latest() ID {
	all := All()
	if len(all) == 0 {
		panic("version: no known builds")
	}
	return all[len(all)-1]
}
