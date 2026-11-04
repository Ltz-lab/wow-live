package bnet

import "fmt"

// Authentication messages (authentication_service.proto,
// authentication_types.proto, authentication_listener.proto, all v2/client) and
// the account handle they reference (common/v2/game_account_handle.proto).

// TitleIDWoW is the fourcc "WoW" as the client stores it, the value of the
// title_id in its Logon request (the u32 at 0x1031d3810, read by the builder
// 0x1013a4824 into LogonRequest field 1).
const TitleIDWoW = 5730135 // 0x00576f57

// PlatformMacARM is the platform string of the Logon request: the builder
// converts the fourcc built from "MacA" (0x1022b4cfc) back to text.
const PlatformMacARM = "MacA"

// GameAccountHandle is bgs.protocol.account.v2.GameAccountHandle.
type GameAccountHandle struct {
	ID      uint64 // field 1
	TitleID uint32 // field 2
	Region  uint32 // field 3
}

func (g GameAccountHandle) encode() []byte {
	var w pbWriter
	w.varint(1, g.ID)
	w.varint(2, uint64(g.TitleID))
	w.varint(3, uint64(g.Region))
	return w.b
}

func decodeGameAccountHandle(b []byte) (GameAccountHandle, error) {
	fs, err := pbParse(b)
	if err != nil {
		return GameAccountHandle{}, err
	}
	var g GameAccountHandle
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "GameAccountHandle.id"); err != nil {
				return g, err
			}
			g.ID = f.V
		case 2:
			if err := f.want(wireVarint, "GameAccountHandle.title_id"); err != nil {
				return g, err
			}
			g.TitleID = uint32(f.V)
		case 3:
			if err := f.want(wireVarint, "GameAccountHandle.region"); err != nil {
				return g, err
			}
			g.Region = uint32(f.V)
		}
	}
	return g, nil
}

// LogonOptions is authentication.v2.client.LogonOptions. The client fills
// auth_token (field 1) and user_agent (field 2) in its Logon builder
// (0x1013a4824); which JSON-flow value it passes as the token is not settled
// by the binary (see docs/EVIDENCE.md E26), so AuthToken is whatever the
// caller supplies.
type LogonOptions struct {
	AuthToken []byte // 1
	// WebCredentials is the login ticket, sent as cached_web_credentials (12) in
	// the request encoding above.
	WebCredentials []byte
	UserAgent      string // 2
	DeviceID       string // 3
	Email          string // 10 (oneof additional_id)
	PhoneNumber    string // 11 (oneof additional_id)
	AuthPlatform   string // 12
}

func (o LogonOptions) encode() []byte {
	var w pbWriter
	if len(o.AuthToken) != 0 {
		w.bytes(1, o.AuthToken)
	}
	if o.UserAgent != "" {
		w.str(2, o.UserAgent)
	}
	if o.DeviceID != "" {
		w.str(3, o.DeviceID)
	}
	if o.Email != "" {
		w.str(10, o.Email)
	}
	if o.PhoneNumber != "" {
		w.str(11, o.PhoneNumber)
	}
	if o.AuthPlatform != "" {
		w.str(12, o.AuthPlatform)
	}
	return w.b
}

// LogonRequest is AuthenticationService.Logon's request, in the server's v1
// shape: program "WoW" (field 1, the FourCC as text), platform, locale,
// application version, and the web credentials the logon ticket goes in.
// The client's own builder writes a title_id varint in field 1 (E26) and the
// two shapes differ; a v1 server reads program as a string.
type LogonRequest struct {
	TitleID            uint32       // 1, varint (v2 shape the client uses, E26)
	Platform           string       // 2
	Locale             string       // 3
	Email              string       // 4
	ApplicationVersion uint32       // 6
	Options            LogonOptions // 10; also carries the web credentials
}

// Encode serializes the request.
func (r LogonRequest) Encode() []byte {
	var w pbWriter
	w.varint(1, uint64(r.TitleID))
	if r.Platform != "" {
		w.str(2, r.Platform)
	}
	if r.Locale != "" {
		w.str(3, r.Locale)
	}
	if r.ApplicationVersion != 0 {
		w.varint(4, uint64(r.ApplicationVersion))
	}
	w.bytes(10, r.Options.encode())
	return w.b
}

// DecodeLogonRequest parses a LogonRequest; the server half for tests.
func DecodeLogonRequest(b []byte) (LogonRequest, error) {
	fs, err := pbParse(b)
	if err != nil {
		return LogonRequest{}, err
	}
	var r LogonRequest
	for _, f := range fs {
		switch f.Num {
		case 1:
			// v1 sends program as a string; a title_id varint is accepted too
			switch f.Wire {
			case wireVarint:
				r.TitleID = uint32(f.V)
			case wireBytes:
				// v1 servers send program as text
			default:
				return r, fmt.Errorf("bnet: LogonRequest.program has wire type %d", f.Wire)
			}
		case 2:
			if err := f.want(wireBytes, "LogonRequest.platform"); err != nil {
				return r, err
			}
			r.Platform = string(f.B)
		case 3:
			if err := f.want(wireBytes, "LogonRequest.locale"); err != nil {
				return r, err
			}
			r.Locale = string(f.B)
		case 4:
			if err := f.want(wireVarint, "LogonRequest.application_version"); err != nil {
				return r, err
			}
			r.ApplicationVersion = uint32(f.V)
		case 10:
			if err := f.want(wireBytes, "LogonRequest.logon_options"); err != nil {
				return r, err
			}
			ofs, err := pbParse(f.B)
			if err != nil {
				return r, err
			}
			for _, of := range ofs {
				switch of.Num {
				case 1:
					r.Options.AuthToken = append([]byte(nil), of.B...)
				case 2:
					r.Options.UserAgent = string(of.B)
				case 3:
					r.Options.DeviceID = string(of.B)
				case 10:
					r.Options.Email = string(of.B)
				case 11:
					r.Options.PhoneNumber = string(of.B)
				case 12:
					r.Options.AuthPlatform = string(of.B)
				}
			}
		}
	}
	return r, nil
}

// LogonRecord is authentication.v2.client.LogonRecord, the payload of
// OnLogonComplete. SessionKey (field 5) is what the client later sends back as
// Param_BnetSessionKey: it is stored by the LogonComplete handler 0x1013a5fd0
// (has-bit 2 of the record, the third string slot, into the global at
// 0x10407e3e8) and read by the realm-join builder 0x1013ad9a8, which passes
// that global to 0x101434b44.
type LogonRecord struct {
	AccountID    uint64              // 1
	GameAccounts []GameAccountHandle // 2
	BattleTag    string              // 3
	GeoIPCountry string              // 4
	SessionKey   []byte              // 5
	EmployeeOnly bool                // 6
	LoginTicket  string              // 7
}

// Encode serializes the record; the server half for tests.
func (r LogonRecord) Encode() []byte {
	var w pbWriter
	if r.AccountID != 0 {
		w.varint(1, r.AccountID)
	}
	for _, g := range r.GameAccounts {
		w.bytes(2, g.encode())
	}
	if r.BattleTag != "" {
		w.str(3, r.BattleTag)
	}
	if r.GeoIPCountry != "" {
		w.str(4, r.GeoIPCountry)
	}
	if len(r.SessionKey) != 0 {
		w.bytes(5, r.SessionKey)
	}
	if r.EmployeeOnly {
		w.boolean(6, true)
	}
	if r.LoginTicket != "" {
		w.str(8, r.LoginTicket)
	}
	return w.b
}

func decodeLogonRecord(b []byte) (LogonRecord, error) {
	fs, err := pbParse(b)
	if err != nil {
		return LogonRecord{}, err
	}
	var r LogonRecord
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "LogonRecord.account_id"); err != nil {
				return r, err
			}
			r.AccountID = f.V
		case 2:
			if err := f.want(wireBytes, "LogonRecord.game_account"); err != nil {
				return r, err
			}
			g, err := decodeGameAccountHandle(f.B)
			if err != nil {
				return r, err
			}
			r.GameAccounts = append(r.GameAccounts, g)
		case 3:
			if err := f.want(wireBytes, "LogonRecord.battle_tag"); err != nil {
				return r, err
			}
			r.BattleTag = string(f.B)
		case 4:
			if err := f.want(wireBytes, "LogonRecord.geoip_country"); err != nil {
				return r, err
			}
			r.GeoIPCountry = string(f.B)
		case 5, 9:
			if err := f.want(wireBytes, "LogonRecord.session_key"); err != nil {
				return r, err
			}
			r.SessionKey = append([]byte(nil), f.B...)
		case 6:
			if err := f.want(wireVarint, "LogonRecord.employee_only_mode"); err != nil {
				return r, err
			}
			r.EmployeeOnly = f.V != 0
		case 8:
			if err := f.want(wireBytes, "LogonRecord.login_ticket"); err != nil {
				return r, err
			}
			r.LoginTicket = string(f.B)
		}
	}
	return r, nil
}

// LogonComplete is authentication.v2.client.LogonCompleteNotification.
type LogonComplete struct {
	ErrorCode uint32      // 1
	Record    LogonRecord // 2
}

// Encode serializes the notification; the server half for tests.
func (n LogonComplete) Encode() []byte {
	var w pbWriter
	if n.ErrorCode != 0 {
		w.varint(1, uint64(n.ErrorCode))
	}
	w.bytes(2, n.Record.Encode())
	return w.b
}

// DecodeLogonComplete parses a LogonCompleteNotification.
func DecodeLogonComplete(b []byte) (LogonComplete, error) {
	fs, err := pbParse(b)
	if err != nil {
		return LogonComplete{}, err
	}
	var n LogonComplete
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "LogonComplete.error_code"); err != nil {
				return n, err
			}
			n.ErrorCode = uint32(f.V)
		case 2:
			if err := f.want(wireBytes, "LogonComplete.record"); err != nil {
				return n, err
			}
			if n.Record, err = decodeLogonRecord(f.B); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}
