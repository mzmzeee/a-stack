package main

import (
	"context"
	"encoding/json"
	"github.com/mzmzeee/a-stack/internal/stack"
	"io"
	"os/exec"
	"runtime"
)

var launchBrowser = func(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Run()
}

func openBrowser(ctx context.Context, client *stack.Client, ref stack.Context, machine, noBrowser bool, stdout, stderr io.Writer) error {
	return openView(ctx, client, ref, viewNotes{}, machine, noBrowser, stdout, stderr)
}

// viewNotes describe an opened view without credentials: fields join the JSON
// data, and the lines frame the URL in plain output.
type viewNotes struct {
	fields        map[string]any
	before, after []string
}

func openView(ctx context.Context, client *stack.Client, ref stack.Context, notes viewNotes, machine, noBrowser bool, stdout, stderr io.Writer) error {
	url, err := client.BrowserURL(ctx, ref)
	if err != nil {
		return err
	}
	if machine {
		data := map[string]any{"url": url}
		for k, v := range notes.fields {
			data[k] = v
		}
		err = json.NewEncoder(stdout).Encode(map[string]any{"data": data})
	} else {
		lines := append(append(append([]string{}, notes.before...), url), notes.after...)
		for _, line := range lines {
			if _, err = io.WriteString(stdout, line+"\n"); err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if !noBrowser && launchBrowser(url) != nil {
		_, _ = io.WriteString(stderr, "Browser launch failed; open the printed URL.\n")
	}
	return nil
}
