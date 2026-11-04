package bnet

import (
	"context"
	"fmt"
)

// ExternalChallenge is authentication.v2.client.ExternalChallengeNotification
// as live Battle.net sends it: payload_type (field 2) "web_auth_url" and the
// URL in payload (field 3). Observed on us.actual.battle.net, 2026-11-04
// (EVIDENCE E32).
type ExternalChallenge struct {
	PayloadType string
	Payload     string
}

// DecodeExternalChallenge parses an ExternalChallengeNotification.
func DecodeExternalChallenge(b []byte) (ExternalChallenge, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ExternalChallenge{}, err
	}
	var c ExternalChallenge
	for _, f := range fs {
		switch f.Num {
		case 2:
			if err := f.want(wireBytes, "ExternalChallengeNotification.payload_type"); err != nil {
				return c, err
			}
			c.PayloadType = string(f.B)
		case 3:
			if err := f.want(wireBytes, "ExternalChallengeNotification.payload"); err != nil {
				return c, err
			}
			c.Payload = string(f.B)
		}
	}
	return c, nil
}

// WebLogin performs the web (Aurora) login at url and returns the token the
// server verifies (the login_ticket).
type WebLogin func(ctx context.Context, url string) (token string, err error)

// LogonExternal is the live v2 logon: Logon without a token, the server's
// OnExternalChallenge with the web_auth_url, the web login there, then
// AuthenticationService.VerifyAuthToken(auth_token = field 1) on the same
// connection, ending in OnLogonComplete. The fork's
// AuthenticationService.cpp implements the same order.
func (c *Client) LogonExternal(ctx context.Context, req LogonRequest, web WebLogin) (LogonRecord, error) {
	done := make(chan LogonComplete, 1)
	chal := make(chan ExternalChallenge, 1)
	bad := make(chan error, 2)
	c.Handle(AuthenticationListener, MethodOnLogonComplete, func(body []byte) {
		n, err := DecodeLogonComplete(body)
		if err != nil {
			bad <- err
			return
		}
		done <- n
	})
	c.Handle(AuthenticationListener, MethodOnExternalChallenge, func(body []byte) {
		x, err := DecodeExternalChallenge(body)
		if err != nil {
			bad <- err
			return
		}
		select {
		case chal <- x:
		default:
		}
	})
	c.Handle(AuthenticationListener, MethodOnLogonQueueEnd, func([]byte) {})
	c.Handle(AuthenticationListener, MethodOnLogonQueueUpdate, func([]byte) {})
	req.Options.AuthToken = nil
	if _, err := c.Call(ctx, AuthenticationService, MethodLogon, req.Encode()); err != nil {
		return LogonRecord{}, err
	}
	for {
		select {
		case x := <-chal:
			if x.PayloadType != "web_auth_url" {
				return LogonRecord{}, fmt.Errorf("bnet: external challenge %q is not a web login", x.PayloadType)
			}
			token, err := web(ctx, x.Payload)
			if err != nil {
				return LogonRecord{}, err
			}
			var w pbWriter
			w.str(1, token)
			if _, err := c.Call(ctx, AuthenticationService, MethodVerifyAuthToken, w.b); err != nil {
				return LogonRecord{}, fmt.Errorf("bnet: VerifyAuthToken: %w", err)
			}
		case n := <-done:
			if n.ErrorCode != 0 {
				return n.Record, &LogonError{Code: n.ErrorCode}
			}
			return n.Record, nil
		case err := <-bad:
			return LogonRecord{}, err
		case <-ctx.Done():
			return LogonRecord{}, ctx.Err()
		case <-c.done:
			return LogonRecord{}, c.Err()
		}
	}
}
