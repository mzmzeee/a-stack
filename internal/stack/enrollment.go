package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Enrollment binds a durable client-held token to one request/database. It lets
// a CLI resume after losing a response without ever saving plaintext server-side.
type Enrollment struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	DatabaseID  string `json:"database_id"`
}

// PrepareAgentCredential writes the new credential file before the request is
// sent. The file also names the Workspace, the Session and, for a register, the
// Agent, so that the CLI can fill in --workspace and --session from it.
func PrepareAgentCredential(path, databaseID, action, operationID string, request any) (string, error) {
	return prepareAgentCredential(path, databaseID, action, operationID, request, "")
}

// PrepareRebindCredential is PrepareAgentCredential for agent rebind, whose
// request names the Agent only by ID; name is the Agent's name for the file.
func PrepareRebindCredential(path, databaseID string, r AgentBindingRequest, name string) (string, error) {
	return prepareAgentCredential(path, databaseID, "agent rebind", r.OperationID, r, name)
}

func prepareAgentCredential(path, databaseID, action, operationID string, request any, name string) (string, error) {
	if !validOperationID(operationID) {
		return "", failure("invalid_input", OperationIDRule)
	}

	if !filepath.IsAbs(path) || !validToken(databaseID) {
		return "", failure("invalid_input", "new credential file must be an absolute file name")
	}

	err := checkPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return "", failure("invalid_input", "create a private credential directory first")
	}

	b, err := json.Marshal(struct {
		Action  string
		Request any
	}{action, request})
	if err != nil {
		return "", failure("invalid_input", "invalid enrollment request")
	}

	sum := sha256.Sum256(b)

	enrollment := Enrollment{OperationID: operationID, Digest: hex.EncodeToString(sum[:]), DatabaseID: databaseID}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		if _, err = statePath(path); err != nil {
			return "", err
		}

		credential := Credential{Kind: AgentCapability, Token: opaqueID(), AgentName: name, Enrollment: &enrollment}
		switch r := request.(type) {
		case AgentRegisterRequest:
			credential.WorkspaceID, credential.SessionID, credential.AgentName = r.WorkspaceID, r.SessionID, r.Name
		case AgentBindingRequest:
			credential.WorkspaceID, credential.SessionID = r.WorkspaceID, r.SessionID
		}

		// The service rejects a bad ID or name with its own message; the file
		// then keeps no context rather than one that ReadCredential refuses.
		if !validContext(credential) {
			credential.WorkspaceID, credential.SessionID, credential.AgentName = "", "", ""
		}

		if err = writePrivate(path, credential, false); err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Code != "conflict" {
				return "", err
			}
		}
	} else if err != nil {
		return "", storageError(err)
	}

	credential, err := ReadCredential(path)
	if err != nil {
		return "", err
	}

	if credential.Kind != AgentCapability || credential.Enrollment == nil || *credential.Enrollment != enrollment {
		return "", failure("operation_conflict", "credential file belongs to a different enrollment; resume its original request or use a new filename")
	}

	return tokenHash(credential.Token), nil
}

func ReadRuntimeBinding(path string) (*RuntimeBinding, error) {
	data, err := privateRead(path)
	if err != nil {
		return nil, err
	}

	var binding RuntimeBinding
	if decodeJSON(data, &binding) != nil {
		return nil, failure("invalid_input", "invalid private runtime binding file")
	}

	if err = validateBinding(&binding, true); err != nil {
		return nil, err
	}

	return &binding, nil
}
