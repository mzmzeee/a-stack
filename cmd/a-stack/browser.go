package main

import (
	"context"
	"encoding/json"
	"github.com/mzmzeee/a-stack/internal/stack"
	"io"
	"os/exec"
)

var launchBrowser = func(url string) error { return exec.Command("xdg-open", url).Run() }

func openBrowser(ctx context.Context, client *stack.Client, ref stack.Context, machine, noBrowser bool, stdout, stderr io.Writer) error {
	url, err := client.BrowserURL(ctx, ref)
	if err != nil {
		return err
	}
	if machine {
		err = json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]string{"url": url}})
	} else {
		_, err = io.WriteString(stdout, url+"\n")
	}
	if err != nil {
		return err
	}
	if !noBrowser && launchBrowser(url) != nil {
		_, _ = io.WriteString(stderr, "Browser launch failed; open the printed URL.\n")
	}
	return nil
}
