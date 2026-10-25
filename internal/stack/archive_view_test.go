package stack

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// The age rule changes no revision, so the snapshot ETag carries an archive
// mark: an open view of a quiet Session loads the Tasks again once a done Task
// is ArchiveAge old, and gets 304 before and after.
func TestSnapshotETagChangesWhenTheAgeRuleArchives(t *testing.T) {
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) {
		moment := start.Add(d)
		fixedClock.Store(&moment)
	}
	t.Cleanup(func() { fixedClock.Store(nil) })
	at(0)
	f := tasksFixture(t)
	done := f.create(t, "Done").ResourceID
	f.assignA(t, done)
	f.progress(t, done, "finished")
	_, token := viewTest(t, f, f.ref(0))
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	// poll is one refresh of app.js: known_revision and the last ETag.
	poll := func(known int64, tag string) (int, string, SessionSnapshot) {
		t.Helper()
		body, _ := json.Marshal(SnapshotRequest{Context: f.ref(0), KnownRevision: known})
		req, _ := http.NewRequest("POST", f.service.Discovery.URL+"/v1/session/snapshot", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
		req.Header.Set("Authorization", "Bearer "+token)
		if tag != "" {
			req.Header.Set("If-None-Match", tag)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var answer struct {
			Data SessionSnapshot `json:"data"`
		}
		if r.StatusCode == 200 && json.NewDecoder(r.Body).Decode(&answer) != nil {
			t.Fatal("snapshot answer")
		}
		return r.StatusCode, r.Header.Get("ETag"), answer.Data
	}
	status, tag, first := poll(0, "")
	revision := first.Session.ReadRevision
	if status != 200 || first.Unchanged {
		t.Fatal("first poll", status)
	}
	at(ArchiveAge - time.Second)
	if status, _, _ = poll(revision, tag); status != 304 {
		t.Fatal("a quiet Session changed before the age rule", status)
	}
	at(ArchiveAge + time.Second)
	status, newTag, answer := poll(revision, tag)
	if status != 200 || answer.Unchanged || answer.Counts == nil || newTag == tag || answer.Session.ReadRevision != revision {
		t.Fatalf("age rule did not reach the view: %d %q %+v", status, newTag, answer)
	}
	if task := f.show(t, done); !task.Archived || task.ArchivedReason != "age" {
		t.Fatal("not archived by age", task.ArchivedReason)
	}
	if status, _, _ = poll(revision, newTag); status != 304 {
		t.Fatal("the new ETag is not stable", status)
	}
	// An ETag from an older service has no mark and gets the full answer once.
	if status, _, answer = poll(revision, `"`+f.ref(0).SessionID+`:1"`); status != 200 || answer.Unchanged {
		t.Fatal("an old ETag kept the view unchanged", status)
	}
}
