package main

import (
	"context"
	"encoding/json"
	"github.com/mzmzeee/a-stack/internal/stack"
	"io"
)

// launchBrowser opens a URL in the default browser: see browser_unix.go and
// browser_windows.go. Tests replace it.
var launchBrowser = openURL

func openBrowser(ctx context.Context, client *stack.Client, ref stack.Context, machine, noBrowser bool, stdout, stderr io.Writer) error {
	return openView(ctx, client, ref, viewNotes{}, machine, noBrowser, stdout, stderr)
}

// viewNotes describe an opened view without credentials: fields join the JSON
// data, and the lines frame the URL in plain output.
type viewNotes struct {
	fields        map[string]any
	before, after []string
	// noView prints the notes without making a view link.
	noView bool
}

func openView(ctx context.Context, client *stack.Client, ref stack.Context, notes viewNotes, machine, noBrowser bool, stdout, stderr io.Writer) error {
	url, err := "", error(nil)
	if !notes.noView {
		if url, err = client.BrowserURL(ctx, ref); err != nil {
			return err
		}
	}
	if machine {
		data := map[string]any{}
		if url != "" {
			data["url"] = url
		}
		for k, v := range notes.fields {
			data[k] = v
		}
		err = json.NewEncoder(stdout).Encode(map[string]any{"data": data})
	} else {
		lines := append([]string{}, notes.before...)
		if url != "" {
			lines = append(lines, url)
		}
		lines = append(lines, notes.after...)
		for _, line := range lines {
			if _, err = io.WriteString(stdout, line+"\n"); err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if url != "" && !noBrowser && launchBrowser(url) != nil {
		_, _ = io.WriteString(stderr, "Browser launch failed; open the printed URL.\n")
	}
	return nil
}
