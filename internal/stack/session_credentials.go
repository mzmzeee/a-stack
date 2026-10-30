package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// FinishTransferredCredential changes only the client routing/context of a
// previously prepared private credential. The original source file stays intact.
func FinishTransferredCredential(path, remote string, ref Context) error {
	c, err := ReadCredential(path)
	if err != nil {
		return err
	}

	if c.Kind != AgentCapability || !validToken(ref.WorkspaceID) || !validToken(ref.SessionID) || (remote != "" && !remoteName.MatchString(remote)) {
		return failure("invalid_input", "invalid transferred credential context")
	}

	c.Remote, c.WorkspaceID, c.SessionID = remote, ref.WorkspaceID, ref.SessionID

	return writePrivate(path, c, true)
}

func JoinRemoteSession(ctx context.Context, remote, session, path, code string) (Context, error) {
	if !validToken(session) {
		return Context{}, failure("invalid_input", "Session ID required")
	}

	return joinRemoteCredential(ctx, remote, session, path, code)
}

func RemoteOwnerCredential(remote string) (string, error) {
	profile, err := ReadRemote(remote)
	if err != nil {
		return "", err
	}

	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(config, "a-stack", "remote-credentials", profile.DatabaseID+"-operator.json"), nil
}

func JoinRemoteOwner(ctx context.Context, remote, code string) error {
	path, err := RemoteOwnerCredential(remote)
	if err != nil {
		return err
	}

	_, err = joinRemoteCredential(ctx, remote, "", path, code)
	return err
}

func joinRemoteCredential(ctx context.Context, remote, session, path, code string) (Context, error) {
	return joinRemoteCredentialUsing(ctx, remote, session, path, code, remoteHTTP())
}

func joinRemoteCredentialUsing(ctx context.Context, remote, session, path, code string, httpClient *http.Client) (Context, error) {
	var ref Context
	profile, err := ReadRemote(remote)
	if err != nil {
		return ref, err
	}

	if !filepath.IsAbs(path) {
		return ref, failure("invalid_input", "Session ID and absolute private credential file required")
	}

	if err = MkdirPrivate(filepath.Dir(path)); err != nil {
		return ref, err
	}

	c, err := ReadCredential(path)
	if err != nil {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			return ref, err
		}

		c = Credential{Kind: OperatorCapability, Token: opaqueID(), SessionID: session, Remote: remote, Enrollment: &Enrollment{DatabaseID: profile.DatabaseID}}
		if err = writePrivate(path, c, false); err != nil {
			return ref, err
		}
	}

	if c.Kind != OperatorCapability || c.Remote != remote || c.SessionID != session || c.Enrollment == nil || c.Enrollment.DatabaseID != profile.DatabaseID {
		return ref, failure("conflict", "credential file belongs to another Session or server")
	}

	if code == "" && session != "" && c.WorkspaceID != "" {
		return Context{WorkspaceID: c.WorkspaceID, SessionID: c.SessionID}, nil
	}

	health, err := discoverRemote(ctx, httpClient, profile.URL)
	if err != nil {
		return ref, err
	}

	if health.DatabaseID != profile.DatabaseID {
		return ref, failure("conflict", "remote database changed")
	}

	client := Client{Discovery: Discovery{URL: profile.URL, DatabaseID: profile.DatabaseID, InstanceID: health.InstanceID}, HTTP: httpClient}

	endpoint := "/v1/session/join"
	if session == "" {
		endpoint = "/v1/connect/join"
	}

	data, err := client.Do(ctx, "POST", endpoint, SessionJoinRequest{SessionID: session, CredentialHash: tokenHash(c.Token), Code: code})
	if err != nil {
		return ref, err
	}

	var envelope struct {
		Data Context `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return ref, failure("storage_unavailable", "invalid join response")
	}

	ref = envelope.Data
	c.WorkspaceID = ref.WorkspaceID

	return ref, writePrivate(path, c, true)
}
