package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"
)

// ReadRelayFile applies the same ownership/no-symlink rules as credentials.
func ReadRelayFile(path string, value any) error {
	data, err := privateReadLimit(path, 64<<10)
	if err != nil {
		return err
	}

	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()

	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return failure("invalid_input", "invalid relay JSON")
	}

	return nil
}

// RelayVerify uses only the caller's locally captured target and executable.
func RelayVerify(ctx context.Context, executable string, b RuntimeBinding) string {
	if b.Notices != "pane" {
		return "unsupported"
	}

	switch newHerdr(executable).verify(ctx, b) {
	case "":
		return ""
	case "identity_changed":
		return "identity_changed"
	case "binding_incomplete", "unsupported_harness", "unsupported_herdr":
		return "unsupported"
	default:
		return "unavailable"
	}
}

func RelayPrompt(ctx context.Context, executable string, b RuntimeBinding, deadline time.Time) NoticeState {
	h := newHerdr(executable)
	if b.Notices != "pane" || h.verify(ctx, b) != "" || !clockNow().Before(deadline) || ctx.Err() != nil {
		return NoticeUncertain
	}

	state, _ := h.submit(ctx, b)
	if state != NoticeSubmitted {
		return NoticeUncertain
	}

	return state
}
