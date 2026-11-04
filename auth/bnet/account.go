package bnet

import "context"

// AccountService v2 method ids (account_service.proto, the fork's
// api/client/v2 stubs). The stock client calls these four right after
// LogonComplete (EVIDENCE.md E37).
const (
	MethodGetAccountInfo            = 101
	MethodGetRestriction            = 104
	MethodGetGameAccountInfo        = 201
	MethodGetGameAccountRestriction = 203
)

// GetAccountInfo sends AccountService.GetAccountInfo with an empty request
// and returns the raw response.
func (c *Client) GetAccountInfo(ctx context.Context) ([]byte, error) {
	return c.Call(ctx, AccountService, MethodGetAccountInfo, nil)
}

// GetRestriction sends AccountService.GetRestriction with an empty request.
func (c *Client) GetRestriction(ctx context.Context) ([]byte, error) {
	return c.Call(ctx, AccountService, MethodGetRestriction, nil)
}

func gameAccountRequest(g GameAccountHandle) []byte {
	var w pbWriter
	w.bytes(1, g.encode())
	return w.b
}

// GetGameAccountInfo sends AccountService.GetGameAccountInfo for g.
func (c *Client) GetGameAccountInfo(ctx context.Context, g GameAccountHandle) ([]byte, error) {
	return c.Call(ctx, AccountService, MethodGetGameAccountInfo, gameAccountRequest(g))
}

// GetGameAccountRestriction sends AccountService.GetGameAccountRestriction
// for g.
func (c *Client) GetGameAccountRestriction(ctx context.Context, g GameAccountHandle) ([]byte, error) {
	return c.Call(ctx, AccountService, MethodGetGameAccountRestriction, gameAccountRequest(g))
}

// DecodeGameAccountName returns GetGameAccountInfoResponse.info.name
// (info = field 1, name = field 2).
func DecodeGameAccountName(b []byte) (string, error) {
	fs, err := pbParse(b)
	if err != nil {
		return "", err
	}
	for _, f := range fs {
		if f.Num != 1 || f.Wire != wireBytes {
			continue
		}
		inner, err := pbParse(f.B)
		if err != nil {
			return "", err
		}
		for _, g := range inner {
			if g.Num == 2 && g.Wire == wireBytes {
				return string(g.B), nil
			}
		}
	}
	return "", nil
}
