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

// URLElicitor relays URL elicitations with state bound to a validated caller.
type URLElicitor = urlelicitation.Elicitor[*providers.UserInfo]

type unavailableRequestState struct{}

func (unavailableRequestState) Error() string {
	return "requestState cannot be verified: URL elicitation relay is disabled"
}

func (unavailableRequestState) JSONRPCError() mcp.JSONRPCError {
	return mcp.JSONRPCError{
		JSONRPC: mcp.JSONRPC_VERSION,
		Error: mcp.JSONRPCErrorDetails{
			Code: mcp.INVALID_PARAMS, Message: unavailableRequestState{}.Error(),
		},
	}
}

// userOf is the caller as the request state is sealed for: the issuer,
// subject and client of the Dex ID token the guard validated.
func userOf(ctx context.Context) *providers.UserInfo {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil
	}
	return &providers.UserInfo{Issuer: id.Issuer, ID: id.Subject, ClientID: id.ClientID, Email: id.Email}
}

// relayURLElicitations validates continuations before handlers and relays the
// first pending URL interaction. Decline and cancel never run the handler.
func relayURLElicitations(elicitor *URLElicitor, log *slog.Logger) mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if elicitor == nil {
				if request.Params.RequestState != "" {
					return nil, mcpserver.NewHandlerError(unavailableRequestState{})
				}
				return next(ctx, request)
			}
			user := userOf(ctx)
			answer, err := elicitor.Result(ctx, request, user)
			if err != nil {
				return nil, err
			}
			if user == nil {
				return next(ctx, request)
			}
			var retry *urlelicitation.Retry
			if answer != nil {
				if retry, err = answer.Retry(); err != nil {
					return nil, err
				}
				if retry.Action != mcp.ElicitationResponseActionAccept {
					return mcp.NewToolResultError(fmt.Sprintf("%s did not run: the URL interaction muster requested was answered with %s.", request.Params.Name, retry.Action)), nil
				}
			}
			relayCtx, relay := agentmanager.WithRelay(ctx, retry)
			result, err := next(relayCtx, request)
			elicitation := relay.Elicitation()
			if elicitation == nil || err != nil || result == nil || !result.IsError {
				return result, err
			}
			relayed, relayErr := elicitor.Relay(ctx, request, user, elicitation.Target, elicitation.Relayed)
			if relayErr != nil && !errors.As(relayErr, new(mcp.URLElicitationRequiredError)) {
				log.WarnContext(ctx, "URL elicitation relay failed", "tool", request.Params.Name, "error", relayErr)
				return nil, relayErr
			}
			return relayed, relayErr
		}
	}
}
