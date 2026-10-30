package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func newJoinCode() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("operating system random source unavailable")
	}

	return base64.RawURLEncoding.EncodeToString(b[:])
}

func connectCodeCommand(ctx context.Context, state string, out io.Writer) error {
	client, err := stack.NewClientContext(ctx, state, "", stack.OperatorCapability)
	if err != nil {
		return err
	}

	code := newJoinCode()

	hash := sha256.Sum256([]byte(code))
	if _, err = client.Do(ctx, "POST", "/v1/connect/code", map[string]string{"code_hash": hex.EncodeToString(hash[:])}); err != nil {
		return err
	}

	return json.NewEncoder(out).Encode(map[string]any{"data": map[string]string{"code": code, "expires_in": "24h"}})
}
