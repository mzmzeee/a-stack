package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func inviteSession(ctx context.Context, client *stack.Client, folder string, args []string, credential, remote string, flags lifecycleFlags, machine bool, out io.Writer) error {
	if len(args) != 1 {
		return invalid("session invite takes one exact Tailscale login")
	}

	var err error
	if remote != "" {
		client, err = sharingRemoteClient(ctx, remote, credential, stack.OperatorCapability)
		if err != nil {
			return err
		}
	}

	if remote == "" {
		remote = client.RemoteName
	}

	if err = fillContext(client.Credential, &flags, "session invite"); err != nil {
		return err
	}

	ref := stack.Context{WorkspaceID: flags.workspace, SessionID: flags.session}
	if remote == "" && client.Credential.SessionID == "" || ref.WorkspaceID == "" || ref.SessionID == "" {
		if remote != "" {
			return missingContext
		}

		h := home{cwd: folder}

		rows, err := h.sessions(ctx, client)
		if err != nil {
			return err
		}

		candidates := []sessionRow{}
		linkError := ""
		for _, row := range rows {
			if flags.session != "" && row.SessionID != flags.session || flags.workspace != "" && row.WorkspaceID != flags.workspace {
				continue
			}

			if flags.session == "" && flags.workspace == "" && !row.CurrentFolder {
				continue
			}

			if row.LinkError != "" {
				linkError = row.LinkError
			}

			if !row.Closed && !row.Unreachable {
				candidates = append(candidates, row)
			}
		}

		if len(candidates) == 0 {
			if linkError != "" {
				return invalid(linkError)
			}

			return invalid("no open Session for this folder or selection; use --workspace and --session to name a shared Session")
		}

		if len(candidates) > 1 {
			var choices strings.Builder
			for _, row := range candidates {
				fmt.Fprintf(&choices, "\n  %s (%s)", printable(row.Name), row.SessionID)
			}

			return invalid("more than one open Session; choose --session ID:" + choices.String())
		}

		row := candidates[0]
		ref = stack.Context{WorkspaceID: row.WorkspaceID, SessionID: row.SessionID}
		if row.Remote != "" {
			if err = checkLinkProfile(row.Remote, row.DatabaseID); err != nil {
				return err
			}

			remote = row.Remote

			client, err = sharingRemoteClient(ctx, remote, row.CredentialFile, stack.OperatorCapability)
			if err != nil {
				return err
			}
		}
	}

	session, err := inspectSession(ctx, client, ref)
	if err != nil {
		return err
	}

	if remote == "" {
		data, err := client.Do(ctx, "GET", "/v1/health", nil)
		if err != nil {
			return err
		}

		health, err := decodeData[stack.Health](data)
		if err != nil {
			return err
		}

		if health.SharedURL == "" {
			return invalid("this Session is local-only; use a-stack session share --remote NAME, or start this service with --share, before inviting")
		}
	}

	operation := flags.operationID
	if operation == "" {
		operation = operationID()
	}

	invitation := newJoinCode()
	digest := sha256.Sum256([]byte(invitation))

	data, err := client.Do(ctx, "POST", "/v1/session/invite", stack.SessionInviteRequest{Context: ref, OperationID: operation, Login: args[0], CodeHash: hex.EncodeToString(digest[:])})
	if err != nil {
		return err
	}

	receipt, err := decodeData[stack.Receipt](data)
	if err != nil {
		return err
	}

	if machine {
		return json.NewEncoder(out).Encode(map[string]any{"data": map[string]any{"receipt": receipt, "workspace_id": ref.WorkspaceID, "session_id": ref.SessionID, "name": session.Name, "code": invitation, "expires_in": "24h"}})
	}

	_, err = fmt.Fprintf(out, "Session: %s\nSession ID: %s\nCode: %s\nExpires in: 24h\n", printable(session.Name), ref.SessionID, invitation)

	return err
}
