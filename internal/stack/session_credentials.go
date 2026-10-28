package stack

import (
	"context"
	"encoding/json"
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

func JoinRemoteSession(ctx context.Context, remote, session, path string) (Context, error) {
	var ref Context
	profile, err := ReadRemote(remote)
	if err != nil {
		return ref, err
	}
	if !validToken(session) || !filepath.IsAbs(path) {
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
	httpClient := remoteHTTP()
	health, err := discoverRemote(ctx, httpClient, profile.URL)
	if err != nil {
		return ref, err
	}
	if health.DatabaseID != profile.DatabaseID {
		return ref, failure("conflict", "remote database changed")
	}
	client := Client{Discovery: Discovery{URL: profile.URL, DatabaseID: profile.DatabaseID, InstanceID: health.InstanceID}, HTTP: httpClient}
	data, err := client.Do(ctx, "POST", "/v1/session/join", SessionJoinRequest{SessionID: session, CredentialHash: tokenHash(c.Token)})
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
