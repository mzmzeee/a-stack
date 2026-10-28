package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type interruptedMailOutput struct{ short bool }

func (w interruptedMailOutput) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestMailCheckpointRequiresCompleteOutput(t *testing.T) {
	tasks, send, check, args := mailCheckFixture(t, 1)
	file := filepath.Join(t.TempDir(), "cursor.json")
	send(tasks[0], "first")
	check(0, "--cursor-file", file)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	send(tasks[0], "second")
	for _, short := range []bool{false, true} {
		var errs bytes.Buffer
		if run(append(slices.Clone(args), "--cursor-file", file), interruptedMailOutput{short}, &errs) == 0 {
			t.Fatal("failed output accepted")
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed output advanced checkpoint", err)
		}
	}
	if newest(check(0, "--cursor-file", file), tasks[0]) != 2 {
		t.Fatal("mail lost after output failure")
	}
}

type exitAfterMailOutput struct{}

func (exitAfterMailOutput) Write(p []byte) (int, error) {
	n, err := os.Stdout.Write(p)
	if err != nil {
		return n, err
	}
	// Let the parent kill this process after output but before cursor publication.
	for {
		time.Sleep(time.Hour)
	}
}

func TestMailCheckpointProcessExit(t *testing.T) {
	if encoded := os.Getenv("A_STACK_TEST_MAIL_CHECK_ARGS"); encoded != "" {
		var args []string
		if json.Unmarshal([]byte(encoded), &args) != nil {
			os.Exit(92)
		}
		os.Exit(run(args, exitAfterMailOutput{}, os.Stderr))
	}
	tasks, send, check, args := mailCheckFixture(t, 1)
	file := filepath.Join(t.TempDir(), "cursor.json")
	send(tasks[0], "before exit")
	encoded, _ := json.Marshal(append(slices.Clone(args), "--cursor-file", file))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMailCheckpointProcessExit$")
	cmd.Env = append(os.Environ(), "A_STACK_TEST_MAIL_CHECK_ARGS="+string(encoded))
	var errs bytes.Buffer
	cmd.Stderr = &errs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out, readErr := bufio.NewReader(stdout).ReadBytes('\n')
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	if readErr != nil || !json.Valid(out) || killErr != nil || waitErr == nil {
		t.Fatal("kill after complete output failed", readErr, killErr, waitErr, errs.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("checkpoint saved before stdout completed", err)
	}
	if newest(check(0, "--cursor-file", file), tasks[0]) != 1 {
		t.Fatal("abrupt exit lost mail")
	}
}

func TestMailCheckpointIdentityMismatch(t *testing.T) {
	tasks, send, check, _ := mailCheckFixture(t, 1)
	file := filepath.Join(t.TempDir(), "cursor.json")
	send(tasks[0], "retained")
	check(0, "--cursor-file", file)
	_, _, other, _ := mailCheckFixture(t, 1)
	other(2, "--cursor-file", file)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]string
	if json.Unmarshal(before, &saved) != nil {
		t.Fatal("checkpoint")
	}
	delete(saved, "identity")
	legacy, _ := json.Marshal(saved)
	if err := os.WriteFile(file, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	check(2, "--cursor-file", file)
	after, _ := os.ReadFile(file)
	if !bytes.Equal(legacy, after) {
		t.Fatal("mismatch modified cursor")
	}
}

func TestMailCheckpointFairUnderContinuousArrivals(t *testing.T) {
	tasks, send, check, _ := mailCheckFixture(t, 4)
	slices.Sort(tasks)
	file := filepath.Join(t.TempDir(), "cursor.json")
	for _, task := range tasks {
		send(task, "initial")
	}
	seen := map[string]bool{}
	for range len(tasks) {
		send(tasks[0], "continuous first-Task traffic")
		page := check(0, "--cursor-file", file, "--limit", "1")
		for _, task := range page.Tasks {
			seen[task.TaskID] = true
		}
		if !page.More {
			t.Fatal("round lost partial backlog")
		}
	}
	if len(seen) != len(tasks) {
		t.Fatal("busy first Task starved another Task", seen)
	}
	if newest(check(0, "--cursor-file", file, "--limit", "1"), tasks[0]) != 2 {
		t.Fatal("round did not return to partial Task")
	}
}
