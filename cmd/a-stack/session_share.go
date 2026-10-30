package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var sharingRemoteClient = stack.NewRemoteClient

func isSharingCommand(command string) bool {
	return command == "session share" || command == "session unshare" || command == "session link" || command == "session invite"
}

func decodeData[T any](data []byte) (T, error) {
	var envelope struct {
		Data T `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err := decoder.Decode(&envelope)
	return envelope.Data, err
}

func shareToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("operating system random source unavailable")
	}

	return base64.RawURLEncoding.EncodeToString(b[:])
}

func localFolderSession(ctx context.Context, client *stack.Client, folder string) (stack.Session, error) {
	workspaces, err := listPages[stack.Workspace](ctx, client, "/v1/workspace/list", stack.Context{})
	if err != nil {
		return stack.Session{}, err
	}

	for _, workspace := range workspaces {
		if workspace.Path != folder {
			continue
		}

		sessions, err := listPages[stack.Session](ctx, client, "/v1/session/list", stack.Context{WorkspaceID: workspace.ID})
		if err != nil {
			return stack.Session{}, err
		}

		var chosen stack.Session
		for _, session := range sessions {
			if !session.Closed && (session.TransferState == "" || session.TransferState == "active") && (chosen.ID == "" || session.UpdatedAt > chosen.UpdatedAt) {
				chosen = session
			}
		}

		if chosen.ID != "" {
			return chosen, nil
		}
	}

	return stack.Session{}, invalid("no open local Session for this folder")
}

func sessionLinks(ctx context.Context, client *stack.Client) ([]stack.SessionLink, error) {
	data, err := client.Do(ctx, "POST", "/v1/session/links", struct{}{})
	if err != nil {
		return nil, err
	}

	return decodeData[[]stack.SessionLink](data)
}

func sharingCommand(ctx context.Context, command string, args []string, state, credential, remote, agentFiles, code string, flags lifecycleFlags, out io.Writer) error {
	if credential != "" {
		absolute, err := filepath.Abs(credential)
		if err != nil {
			return err
		}

		credential = absolute
	}

	folder, err := os.Getwd()
	if err != nil {
		return err
	}

	folder, err = stack.ResolvePath(folder)
	if err != nil {
		return err
	}

	local, err := stack.NewClient(state, "", stack.OperatorCapability)
	if err != nil {
		if command == "session link" {
			return invalid(fmt.Sprintf("session link needs a running local service; run a-stack --state-dir %q service install, then retry", state))
		}

		return err
	}

	private, err := transferDirectory()
	if err != nil {
		return err
	}

	if command == "session link" {
		if _, err = sessionLinks(ctx, local); err != nil {
			return invalid(fmt.Sprintf("session link cannot reach the local service; run a-stack --state-dir %q service install, then retry", state))
		}

		if len(args) != 2 {
			return invalid("session link takes REMOTE-or-URL SESSION")
		}

		name := args[0]
		if strings.Contains(name, "://") {
			profiles, e := stack.ListRemotes()
			if e != nil {
				return e
			}

			name = ""
			for _, profile := range profiles {
				if profile.URL == args[0] {
					name = profile.Name
				}
			}

			if name == "" {
				return invalid("connect the server URL to a named remote first")
			}
		}

		profile, e := stack.ReadRemote(name)
		if e != nil {
			return e
		}

		file := filepath.Join(private, profile.DatabaseID+"-"+args[1]+"-operator.json")

		ref, e := stack.JoinRemoteSession(ctx, name, args[1], file, code)
		if e != nil {
			return e
		}

		server, e := sharingRemoteClient(ctx, name, file, stack.OperatorCapability)
		if e != nil {
			return e
		}

		session, e := inspectSession(ctx, server, ref)
		if e != nil {
			return e
		}

		data, e := local.Do(ctx, "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: folder, Remote: name, DatabaseID: profile.DatabaseID, CredentialFile: file, Name: session.Name}})
		if e != nil {
			return e
		}

		return writeData(out, data)
	}

	if command == "session invite" {
		if len(args) != 1 {
			return invalid("session invite takes one exact Tailscale login")
		}

		client := local
		ref := stack.Context{WorkspaceID: flags.workspace, SessionID: flags.session}

		if remote != "" {
			client, err = sharingRemoteClient(ctx, remote, credential, stack.OperatorCapability)
			if err != nil {
				return err
			}
		}

		if ref.SessionID == "" {
			links, e := sessionLinks(ctx, local)
			if e != nil {
				return e
			}

			for _, link := range links {
				if link.Path == folder {
					ref = link.Context
					client, err = sharingRemoteClient(ctx, link.Remote, link.CredentialFile, stack.OperatorCapability)
					break
				}
			}

			if err != nil {
				return err
			}

			if ref.SessionID == "" {
				session, e := localFolderSession(ctx, local, folder)
				if e != nil {
					return e
				}

				ref = stack.Context{WorkspaceID: session.WorkspaceID, SessionID: session.ID}
			}
		}

		operation := flags.operationID
		if operation == "" {
			operation = operationID()
		}

		invitation := newJoinCode()
		digest := sha256.Sum256([]byte(invitation))

		data, e := client.Do(ctx, "POST", "/v1/session/invite", stack.SessionInviteRequest{Context: ref, OperationID: operation, Login: args[0], CodeHash: hex.EncodeToString(digest[:])})
		if e != nil {
			return e
		}

		receipt, e := decodeData[stack.Receipt](data)
		if e != nil {
			return e
		}

		return json.NewEncoder(out).Encode(map[string]any{"data": map[string]any{"receipt": receipt, "code": invitation, "expires_in": "24h"}})
	}

	if len(args) != 0 {
		return invalid(command + " takes no positional arguments")
	}

	journalKey := sha256.Sum256([]byte(folder + "\x00" + command))
	journalFile := filepath.Join(private, hex.EncodeToString(journalKey[:])+".json")

	lockDir := transferLockDirectory(private, folder)
	if err = stack.MkdirPrivate(lockDir); err != nil {
		return err
	}

	release, locked := tryLock(lockDir)
	if !locked {
		return invalid("another transfer is using this folder; retry when it finishes")
	}

	defer release()

	var journal shareJournal
	if data, e := os.ReadFile(journalFile); e == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()

		if decoder.Decode(&journal) != nil || journal.Folder != folder || journal.Command != command {
			return invalid("invalid transfer recovery journal")
		}
	} else if !os.IsNotExist(e) {
		return e
	}

	if !journal.Started && journal.Bundle.TransferID == "" {
		journal = shareJournal{}
	}

	if journal.ID == "" {
		journal = shareJournal{ID: shareToken(), Folder: folder, Command: command, Remote: remote, RemoteCredential: credential}
		if command == "session unshare" {
			links, e := sessionLinks(ctx, local)
			if e != nil {
				return e
			}

			for _, link := range links {
				if link.Path == folder {
					journal.Remote = link.Remote
					journal.RemoteCredential = link.CredentialFile
					journal.Source = link.Context
					journal.Link = link
					break
				}
			}

			if journal.Source.SessionID == "" {
				return invalid("no linked Session for this folder")
			}
		} else {
			if remote == "" {
				profiles, e := stack.ListRemotes()
				if e != nil {
					return e
				}

				if len(profiles) != 1 {
					return invalid("session share requires --remote NAME when there is not exactly one profile")
				}

				journal.Remote = profiles[0].Name
			}

			session, e := localFolderSession(ctx, local, folder)
			if e != nil {
				return e
			}

			journal.Source = stack.Context{WorkspaceID: session.WorkspaceID, SessionID: session.ID}
		}

		cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
		cmd.Dir = folder

		raw, e := cmd.Output()
		if e != nil {
			return invalid("sharing requires a Git origin remote")
		}

		journal.Repository, e = stack.RepositoryIdentity(strings.TrimSpace(string(raw)))
		if e != nil {
			return e
		}
	}

	if journal.Started && ((remote != "" && remote != journal.Remote) || (credential != "" && credential != journal.RemoteCredential)) {
		return invalid("transfer connection is pinned; retry with its original remote and credential")
	}

	server, err := sharingRemoteClient(ctx, journal.Remote, journal.RemoteCredential, stack.OperatorCapability)
	if err != nil {
		return err
	}

	if journal.RemoteCredential == "" {
		journal.RemoteCredential, err = stack.RemoteOwnerCredential(journal.Remote)
		if err != nil {
			return err
		}
	}

	source, dest := local, server
	targetRemote := journal.Remote
	if command == "session unshare" {
		source, dest = server, local
		targetRemote = ""
	}

	if journal.Started && (journal.SourceDatabase != source.Discovery.DatabaseID || journal.DestinationDatabase != dest.Discovery.DatabaseID) {
		return invalid("transfer database identity changed")
	}

	journal.Started = true
	journal.SourceDatabase, journal.DestinationDatabase = source.Discovery.DatabaseID, dest.Discovery.DatabaseID
	if err = saveShareJournal(journalFile, journal); err != nil {
		return err
	}

	if journal.Bundle.TransferID == "" {
		data, e := source.Do(ctx, "POST", "/v1/session/transfer/export", stack.TransferExportRequest{Context: journal.Source, TransferID: journal.ID, Repository: journal.Repository})
		if e != nil {
			return e
		}

		journal.Bundle, e = decodeData[stack.TransferBundle](data)
		if e != nil {
			return e
		}

		journal.Agents, e = prepareShareAgents(ctx, state, source, dest, journal.Bundle, agentFiles)
		if e != nil {
			return e
		}

		if err = saveShareJournal(journalFile, journal); err != nil {
			return err
		}
	}

	hashes := map[string]string{}
	for id, agent := range journal.Agents {
		hashes[id] = agent.Hash
	}

	if journal.Destination.SessionID == "" {
		host, e := os.Hostname()
		if e != nil {
			return e
		}

		request := stack.TransferImportRequest{Bundle: journal.Bundle, AgentHashes: hashes, Machine: host, LocalRuntime: command == "session unshare"}
		if command == "session unshare" {
			request.WorkspacePath = folder
		}

		data, e := dest.Do(ctx, "POST", "/v1/session/transfer/import", request)
		if e != nil {
			return e
		}

		status, e := decodeData[stack.TransferStatus](data)
		if e != nil {
			return e
		}

		journal.Destination = status.Context
		if err = saveShareJournal(journalFile, journal); err != nil {
			return err
		}
	}

	// Files are prepared before sealing. Retrying the same command completes a
	// lost response; it never unseals a source whose destination may be active.
	for _, agent := range journal.Agents {
		if err = stack.FinishTransferredCredential(agent.New, targetRemote, journal.Destination); err != nil {
			return err
		}
	}

	if _, err = source.Do(ctx, "POST", "/v1/session/transfer/seal", stack.TransferChangeRequest{Context: journal.Source, TransferID: journal.ID, DestinationDatabase: dest.Discovery.DatabaseID, Remote: targetRemote}); err != nil {
		if e, ok := err.(*stack.Error); ok && e.Code == "conflict" && strings.Contains(e.Message, "changed after export") {
			if removeErr := os.Remove(journalFile); removeErr != nil {
				return removeErr
			}

			return invalid("Session changed during transfer; source is intact. Rerun this command to replace the read-only staged import with a fresh snapshot")
		}

		return err
	}

	if _, err = dest.Do(ctx, "POST", "/v1/session/transfer/activate", stack.TransferChangeRequest{Context: journal.Destination, TransferID: journal.ID}); err != nil {
		return err
	}

	if err = migrateShareHooks(journal.Agents, journal.Source, journal.Destination); err != nil {
		return fmt.Errorf("transfer activated; rerun this command to finish hook migration: %w", err)
	}

	if command == "session share" {
		link := stack.SessionLink{Context: journal.Destination, Path: folder, Remote: journal.Remote, DatabaseID: dest.Discovery.DatabaseID, CredentialFile: journal.RemoteCredential, Name: journal.Bundle.Session.Name}
		if _, err = local.Do(ctx, "POST", "/v1/session/link", stack.SessionLinkRequest{Link: link}); err != nil {
			return err
		}
	} else {
		if _, err = local.Do(ctx, "POST", "/v1/session/link", stack.SessionLinkRequest{Link: journal.Link, Remove: true}); err != nil {
			return err
		}
	}

	if err = os.Remove(journalFile); err != nil {
		return err
	}

	return json.NewEncoder(out).Encode(map[string]any{"data": map[string]any{"transfer_id": journal.ID, "session_id": journal.Destination.SessionID, "workspace_id": journal.Destination.WorkspaceID, "outcome": strings.TrimPrefix(command, "session ") + "d"}})
}

type (
	shareAgent   struct{ Old, New, Hash string }
	shareJournal struct {
		Started                                                   bool
		SourceDatabase, DestinationDatabase                       string
		ID, Folder, Command, Remote, RemoteCredential, Repository string
		Source, Destination                                       stack.Context
		Bundle                                                    stack.TransferBundle
		Agents                                                    map[string]shareAgent
		Link                                                      stack.SessionLink
	}
)

func transferDirectory() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(config, "a-stack", "transfers")
	return dir, stack.MkdirPrivate(dir)
}

func transferLockDirectory(private, folder string) string {
	key := sha256.Sum256([]byte(folder))
	return filepath.Join(private, hex.EncodeToString(key[:])+".lock")
}

func saveShareJournal(file string, journal shareJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}

	return writeAtomic(file, data, 0o600)
}
