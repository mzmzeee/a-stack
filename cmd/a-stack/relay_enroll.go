package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type relayEnrollment struct {
	Database, Agent, Credential, Machine, Proof, Operation, StateDir, Worktree, Remote string
	Context                                                                            stack.Context
	BaseRevision, Revision                                                             int64
	Binding                                                                            stack.RuntimeBinding
}
type relayInterval struct {
	Version              int
	CheckedAt, NotBefore int64
}

func relayHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func relayRoot() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(root, "a-stack", "relay"), nil
}

func relayEnrollmentFile(credential string) (string, error) {
	root, err := relayRoot()
	if err != nil {
		return "", err
	}

	absolute, err := filepath.Abs(credential)
	if err != nil {
		return "", err
	}

	return filepath.Join(root, "enroll-"+relayHash(absolute)+".json"), nil
}

func relayIntervalDir(e relayEnrollment) (string, error) {
	root, err := relayRoot()
	if err != nil {
		return "", err
	}

	if !stack.ValidID(e.Database) || !stack.ValidID(e.Agent) {
		return "", invalid("invalid relay enrollment identity")
	}

	return filepath.Join(root, "interval-"+relayHash(e.Database+":"+e.Agent)), nil
}

func readRelayInterval(dir string) (relayInterval, error) {
	var state relayInterval
	err := stack.ReadRelayFile(filepath.Join(dir, "interval.json"), &state)
	if err != nil {
		return state, err
	}

	if state.Version != 1 || state.CheckedAt < 0 || state.NotBefore < state.CheckedAt {
		return state, invalid("corrupt relay interval; explicit repair required")
	}

	return state, nil
}

func relayBind(ctx context.Context, client *stack.Client, f lifecycleFlags, executable, credential string) ([]byte, error) {
	if client.Credential.Kind != stack.AgentCapability || credential == "" || f.notices != "pane" || f.revision < 1 || f.operationID == "" {
		return nil, invalid("relay enrollment requires own Agent credential, revision, operation ID and explicit --notices pane")
	}

	file, err := relayEnrollmentFile(credential)
	if err != nil {
		return nil, err
	}

	root := filepath.Dir(file)
	if err = stack.EnsurePrivateDirectory(root); err != nil {
		return nil, err
	}

	unlock, ok := tryLock(root)
	if !ok {
		return nil, invalid("relay enrollment is busy")
	}

	defer unlock()
	generation := credentialGeneration(credential)

	machine, err := hookMachine(true)
	if err != nil {
		return nil, err
	}

	var e relayEnrollment
	if _, err = os.Lstat(file); err == nil {
		if err = stack.ReadRelayFile(file, &e); err != nil {
			return nil, err
		}

		if e.Operation != f.operationID || e.BaseRevision != f.revision || e.Credential != generation || e.Context != (stack.Context{WorkspaceID: f.workspace, SessionID: f.session}) || e.Agent != f.agent || e.Database != client.Discovery.DatabaseID || e.Machine != relayHash(machine) {
			return nil, invalid("existing relay capture differs; use Operator clear/credential rotation and a fresh credential file")
		}
	} else if os.IsNotExist(err) {
		name, lookupErr := agentName(ctx, client, stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, f.agent)
		if lookupErr != nil {
			return nil, lookupErr
		}

		if executable == "" {
			executable = "herdr"
		}

		binding, bindErr := stack.HerdrBind(ctx, executable, name, os.Getenv)
		if bindErr != nil {
			return nil, bindErr
		}

		binding.Notices = "pane"
		if reason := stack.RelayVerify(ctx, executable, *binding); reason != "" {
			return nil, invalid("relay target is not ready for enrollment: " + reason)
		}

		proof := shareToken()
		binding.RelayMachine = relayHash(machine)
		binding.RelayKeyHash = relayHash(proof)

		worktree, wdErr := os.Getwd()
		if wdErr != nil {
			return nil, wdErr
		}

		e = relayEnrollment{Database: client.Discovery.DatabaseID, Agent: f.agent, Credential: generation, Machine: relayHash(machine), Proof: proof, Operation: f.operationID, StateDir: client.StateDir, Worktree: worktree, Remote: client.RemoteName, Context: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, BaseRevision: f.revision, Binding: *binding}

		dir, dirErr := relayIntervalDir(e)
		if dirErr != nil {
			return nil, dirErr
		}

		if dirErr = os.Mkdir(dir, 0o700); dirErr == nil {
			if err = stack.ReplacePrivate(filepath.Join(dir, "interval.json"), relayInterval{Version: 1}); err != nil {
				return nil, err
			}
		} else if !os.IsExist(dirErr) {
			return nil, dirErr
		}

		if _, err = readRelayInterval(dir); err != nil {
			return nil, err
		}

		if err = stack.ReplacePrivate(file, e); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	data, err := client.Do(ctx, "POST", "/v1/herdr/bind", stack.AgentBindingRequest{Context: e.Context, OperationID: e.Operation, AgentID: e.Agent, ExpectedRevision: e.BaseRevision, Runtime: &e.Binding})
	if err != nil {
		return nil, err
	}

	var result struct{ Data stack.Receipt }
	if json.Unmarshal(data, &result) != nil || result.Data.Revision != e.BaseRevision+1 {
		return nil, invalid("unexpected relay attach receipt")
	}

	e.Revision = result.Data.Revision
	if err = stack.ReplacePrivate(file, e); err != nil {
		return nil, err
	}

	native, err := hookNativeSession(&e.Binding)
	if err != nil {
		return nil, err
	}

	id := hookIdentity{credential: credential, agent: e.Agent, ref: e.Context, worktree: e.Worktree, runtimeID: e.Binding.InstanceID}
	if err = enrollHook(id, e.Binding.Harness, native, "", e.Revision); err != nil {
		return nil, err
	}

	return data, nil
}

func reserveRelayInterval(dir string, clock time.Time) (bool, error) {
	state, err := readRelayInterval(dir)
	if err != nil {
		return false, err
	}

	now := clock.UnixMilli()
	if now < state.CheckedAt {
		return false, invalid("relay clock moved backwards; explicit repair required")
	}

	if now < state.NotBefore {
		return false, nil
	}

	return true, stack.ReplacePrivate(filepath.Join(dir, "interval.json"), relayInterval{Version: 1, CheckedAt: now, NotBefore: clock.Add(11 * time.Minute).UnixMilli()})
}
