package realm

// The ClientInformation object of Param_ClientInfo.
//
// The framing is E26's (TypeName ':' json NUL); the object itself is the shape
// of JSON.RealmList.ClientInformation in the fork's RealmList.proto, which its
// bnetserver parses field by field (GameUtilitiesService.cpp GetRealmListTicket:
// it reads info().secret(), info().platformtype(), info().clientarch() and
// info().type(), and refuses the ticket without a 32-byte secret). The client
// fills the same members from runtime values (0x1014333f4) but does not name
// them, so the member names here are the fork's.
//
// platform, type, platformType, clientArch and systemArch are FourCC values
// (ClientBuildInfo.h): the client's own MacA / WoW / Mac / A64 / A64 for this
// build. secret is 32 random bytes per connection, matching the fork's
// per-session client secret.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

// SecretLen is the exact length of the client secret (the fork wants 32).
const SecretLen = 32

// Values as the 70707/70755 ticket builder (0x1014333f4) fills them (EVIDENCE
// E32): platform "MacA" (0x1022b4cfc), type "WoW" (set by 0x1014cbde4 at
// startup), buildVariant the literal at 0x1031d3828, platformType and
// systemArch the 8 bytes at 0x10318f5e0 ("caM\0" "46A\0" read as u32 LE),
// clientArch from the table at 0x10318f604, locales as big-endian FourCCs of
// the locale name (0x100527928), timeZone the literal "Etc/UTC" and
// systemVersion "%u.%u.%u.%u" of the OS version (0x102e88c40).
const (
	PlatformMacARM  uint32 = 0x4D616341 // "MacA"
	PlatformTypeMac uint32 = 0x004D6163 // "Mac"
	ArchARM64       uint32 = 0x413634   // "A64"
	TypeWoW         uint32 = 0x576F57   // "WoW"
	LocaleEnUSText  uint32 = 0x656E5553 // "enUS"
	LocaleEnUSAudio uint32 = 0x656E5553 // "enUS"
	DefaultTimeZone        = "Etc/UTC"
	// DefaultSystemVer is what the stock 70755 client sent from this Mac
	// (macOS 27.2, build 26B123) in its realm ticket (E37); the fourth part is
	// not the zero the format string suggested.
	DefaultSystemVer    = "27.2.0.131524635"
	DefaultBuildVariant = "darwin-ARM64-clang-release"
)

// dataBuilds maps a client build to the versionDataBuild the stock client of
// that build sends (E37: 70755 sends 61631, not its own build number).
var dataBuilds = map[int]uint32{70755: 61631}

// DataBuild returns the versionDataBuild for build.
func DataBuild(build int) uint32 {
	if d, ok := dataBuilds[build]; ok {
		return d
	}
	return uint32(build)
}

// ErrSecret means the client secret is not SecretLen bytes.
var ErrSecret = errors.New("realm: client secret must be 32 bytes")

// clientVersion is JSON.RealmList.ClientVersion.
type clientVersion struct {
	Major    uint32 `json:"versionMajor"`
	Minor    uint32 `json:"versionMinor"`
	Revision uint32 `json:"versionRevision"`
	Build    uint32 `json:"versionBuild"`
}

// clientInformation is JSON.RealmList.ClientInformation. Every member is
// required by the fork's proto, so all of them are always sent.
type clientInformation struct {
	Platform         uint32        `json:"platform"`
	BuildVariant     string        `json:"buildVariant"`
	Type             uint32        `json:"type"`
	TimeZone         string        `json:"timeZone"`
	CurrentTime      uint32        `json:"currentTime"`
	TextLocale       uint32        `json:"textLocale"`
	AudioLocale      uint32        `json:"audioLocale"`
	VersionDataBuild uint32        `json:"versionDataBuild"`
	Version          clientVersion `json:"version"`
	Secret           []int         `json:"secret"`
	ClientArch       uint32        `json:"clientArch"`
	SystemVersion    string        `json:"systemVersion"`
	PlatformType     uint32        `json:"platformType"`
	SystemArch       uint32        `json:"systemArch"`
}

// secretInts renders the 32 secret bytes as the JSON array of byte values the
// fork's ClientInformation reads ([]int, not base64: Go would base64-encode a
// []byte field, and the server reads secret only as an array of numbers).
func secretInts(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// ClientInfo is the caller-facing form: the values that differ per run.
type ClientInfo struct {
	// Secret is the 32-byte client secret; nil means fresh random bytes.
	Secret []byte
	// Now is the currentTime; zero means time.Now().
	Now time.Time
	// Version defaults to the reference build (version.Forever()).
	Version *version.ID
	// TimeZone, SystemVersion, BuildVariant and the locale ids default to the
	// constants above and may be overridden.
	TimeZone      string
	SystemVersion string
	BuildVariant  string
	TextLocale    uint32
	AudioLocale   uint32
	// VersionDataBuild overrides DataBuild(Version.Build) when non-zero.
	VersionDataBuild uint32
}

// JSON renders the object, with fresh random bytes and the current time when
// the caller left them out.
func (c ClientInfo) JSON() (json.RawMessage, error) {
	secret := c.Secret
	if secret == nil {
		secret = make([]byte, SecretLen)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	}
	if len(secret) != SecretLen {
		return nil, fmt.Errorf("%w: got %d", ErrSecret, len(secret))
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	v := version.Forever()
	if c.Version != nil {
		v = *c.Version
	}
	tz, sys, variant := c.TimeZone, c.SystemVersion, c.BuildVariant
	if tz == "" {
		tz = DefaultTimeZone
	}
	if sys == "" {
		sys = DefaultSystemVer
	}
	if variant == "" {
		variant = DefaultBuildVariant
	}
	textLocale, audioLocale := c.TextLocale, c.AudioLocale
	if textLocale == 0 {
		textLocale = LocaleEnUSText
	}
	if audioLocale == 0 {
		audioLocale = LocaleEnUSAudio
	}
	dataBuild := c.VersionDataBuild
	if dataBuild == 0 {
		dataBuild = DataBuild(int(v.Build))
	}
	info := clientInformation{
		Platform:         PlatformMacARM,
		BuildVariant:     variant,
		Type:             TypeWoW,
		TimeZone:         tz,
		CurrentTime:      uint32(now.Unix()),
		TextLocale:       textLocale,
		AudioLocale:      audioLocale,
		VersionDataBuild: dataBuild,
		Version: clientVersion{
			Major:    uint32(v.Major),
			Minor:    uint32(v.Minor),
			Revision: uint32(v.Patch),
			Build:    uint32(v.Build),
		},
		Secret:        secretInts(secret),
		ClientArch:    ArchARM64,
		SystemVersion: sys,
		PlatformType:  PlatformTypeMac,
		SystemArch:    ArchARM64,
	}
	// RealmListTicketClientInformation wraps ClientInformation in "info".
	return json.Marshal(struct {
		Info clientInformation `json:"info"`
	}{info})
}
