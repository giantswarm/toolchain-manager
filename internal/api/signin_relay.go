package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-toolkit/urlelicitation"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/toolchain-manager/internal/agentmanager"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

// SignInElicitor asks a tool's caller to open the sign-in muster asked for,
// sealing requestState for the caller's validated token.
type SignInElicitor = urlelicitation.Elicitor[*providers.UserInfo]

// userOf is the caller as the request state is sealed for: the issuer,
// subject and client of the Dex ID token the guard validated.
func userOf(ctx context.Context) *providers.UserInfo {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil
	}
	return &providers.UserInfo{Issuer: id.Issuer, ID: id.Subject, ClientID: id.Client, Email: id.Email}
}

// relaySignIns runs a tool call on an agentmanager.Relay. A retry that
// answers a relayed sign-in with decline or cancel runs nothing; accept goes
// to muster's call on the retry. A call that fails after muster asked for a
// sign-in answers with that sign-in, as urlelicitation answers the caller's
// protocol revision. A caller without a validated identity keeps the tool
// error.
func relaySignIns(elicitor *SignInElicitor, log *slog.Logger) mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			user := userOf(ctx)
			if user == nil {
				return next(ctx, req)
			}
			answer, err := elicitor.Result(ctx, req, user)
			if err != nil {
				return nil, err
			}
			var retry *urlelicitation.Retry
			if answer != nil {
				if retry, err = answer.Retry(); err != nil {
					return nil, err
				}
				if retry.Action != mcp.ElicitationResponseActionAccept {
					return mcp.NewToolResultError(fmt.Sprintf("%s did not run: the sign-in muster asked for was answered with %s.", req.Params.Name, retry.Action)), nil
				}
			}
			relayCtx, relay := agentmanager.WithRelay(ctx, retry)
			res, err := next(relayCtx, req)
			signIn := relay.SignIn()
			if signIn == nil || err != nil || res == nil || !res.IsError {
				return res, err
			}
			relayed, relayErr := elicitor.Relay(ctx, req, user, signIn.Target, signIn.Relayed)
			if relayErr != nil && !errors.As(relayErr, new(mcp.URLElicitationRequiredError)) {
				log.WarnContext(ctx, "muster sign-in not relayed: answered as a tool error", "tool", req.Params.Name, "error", relayErr)
				return res, nil
			}
			return relayed, relayErr
		}
	}
}
