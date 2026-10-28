package main

import (
	"context"
	"encoding/json"
	"io"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func connectCommand(args []string, stdout io.Writer) error {
	var data any
	var err error
	switch {
	case len(args) == 1 && args[0] == "list":
		data, err = stack.ListRemotes()
	case len(args) == 2 && args[0] == "remove":
		err = stack.RemoveRemote(args[1])
		data = map[string]string{"removed": args[1]}
	case len(args) == 2:
		data, err = stack.ConnectRemote(context.Background(), args[0], args[1])
	default:
		return invalid("use connect NAME URL, connect list, or connect remove NAME")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"data": data})
}

func configureServiceShare(state string, data map[string]any) error {
	enabled, ok := data["share"].(bool)
	if !ok {
		return nil
	}
	if !enabled {
		if err := stack.DisableShare(context.Background(), state); err != nil {
			return err
		}
	}
	return stack.SaveShareSetting(state, enabled)
}

func commandClient(ctx context.Context, state, credential, kind, remote string) (*stack.Client, error) {
	if remote != "" {
		return stack.NewRemoteClient(ctx, remote, credential, stack.CapabilityKind(kind))
	}
	return stack.NewClientContext(ctx, state, credential, stack.CapabilityKind(kind))
}
