package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartRefusesEmptyInitializedDatabase(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "missing"}[missing], func(t *testing.T) {
			service, dir, _ := startTest(t)
			path := filepath.Join(dir, "state.db")
			if err := service.Close(ctx); err != nil {
				t.Fatal(err)
			}
			var err error
			if missing {
				err = os.Remove(path)
			} else {
				err = os.Truncate(path, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := Start(ctx, dir, "")
			if restarted != nil {
				_ = restarted.Close(ctx)
			}
			requireUnusable(t, err, path, "restore a backup")
			info, err := os.Stat(path)
			if err == nil && info.Size() != 0 {
				t.Fatal("refused database was initialized")
			}
		})
	}
}

func TestStartAfterInterruptedInitialization(t *testing.T) {
	for _, phase := range []string{"new", "empty-file", "schema", "credential-file"} {
		t.Run(phase, func(t *testing.T) {
			dir := privateTestDir(t)
			var id string
			if phase != "new" {
				path, err := ensureDatabaseFile(dir)
				if err != nil {
					t.Fatal(err)
				}
				if phase != "empty-file" {
					s, err := openStore(ctx, path)
					if err != nil {
						t.Fatal(err)
					}
					id = s.id
					if err = s.db.Close(); err != nil {
						t.Fatal(err)
					}
					if phase == "credential-file" {
						if err = writePrivate(filepath.Join(dir, "operator.json"), Credential{Kind: OperatorCapability, Token: opaqueID()}, false); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			s, err := Start(ctx, dir, "")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(ctx)
			if id != "" && s.Store.id != id {
				t.Fatal("interrupted bootstrap changed database identity")
			}
			credential, err := ReadCredential(filepath.Join(dir, "operator.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = authenticate(ctx, s.Store.db, credential.Token); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStartRefusesDamagedDatabaseHeader(t *testing.T) {
	s, dir, _ := startTest(t)
	path := filepath.Join(dir, "state.db")
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("broken sqlite header"), 0); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Start(ctx, dir, "")
	if restarted != nil {
		_ = restarted.Close(ctx)
	}
	requireUnusable(t, err, path, "restore a backup")
}
