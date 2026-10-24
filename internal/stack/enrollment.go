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

func PrepareAgentCredential(path, databaseID, action, operationID string, request any) (string, error) {
	if !filepath.IsAbs(path) || !validToken(databaseID) || !validOperationID(operationID) {
		return "", failure("invalid_input", "new private credential filename and operation ID required")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return "", failure("invalid_input", "create a private credential directory first")
	}
	if err = checkPrivate(parent, true); err != nil {
		return "", err
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
		credential := Credential{Kind: AgentCapability, Token: opaqueID(), Enrollment: &enrollment}
		if err = writePrivate(path, credential, false); err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Code != "conflict" {
				return "", err
			}
		}
	} else if err != nil {
		return "", storageError()
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
