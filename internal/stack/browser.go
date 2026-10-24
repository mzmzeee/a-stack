package stack

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed browser/*
var browserFiles embed.FS
var browserTemplate = template.Must(template.ParseFS(browserFiles, "browser/index.html"))

type BrowserOpenRequest struct {
	Context
	OperationID    string `json:"operation_id"`
	CredentialHash string `json:"credential_hash"`
}

// OpenBrowser delegates only the caller's existing Session metadata read scope.
// The caller generates the secret: neither receipts nor the service receive it.
func (s *Store) OpenBrowser(ctx context.Context, token string, r BrowserOpenRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "browser.open", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		if p.Kind == BrowserCapability {
			return failure("forbidden", "browser credentials cannot delegate")
		}
		return replaySession(ctx, tx, p, receipt)
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if p.Kind == BrowserCapability {
			return Receipt{}, failure("forbidden", "browser credentials cannot delegate")
		}
		session, err := ReadSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if !validHash(r.CredentialHash) {
			return Receipt{}, failure("invalid_input", "credential_hash must be a SHA-256 lowercase hex digest")
		}
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM capabilities WHERE token_hash=?", r.CredentialHash).Scan(&exists); err != nil {
			return Receipt{}, err
		}
		if exists != 0 {
			return Receipt{}, failure("conflict", "credential hash already exists")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,?,?,?)", r.CredentialHash, BrowserCapability, opaqueID(), session.ID)
		if err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: session.ID, Revision: session.Revision, Outcome: "browser-read-issued"}, nil
	})
}

func (c *Client) BrowserURL(ctx context.Context, ref Context) (string, error) {
	secret := opaqueID()
	_, err := c.Do(ctx, "POST", "/v1/browser/open", BrowserOpenRequest{Context: ref, OperationID: opaqueID(), CredentialHash: tokenHash(secret)})
	if err != nil {
		return "", err
	}
	return c.Discovery.URL + "/view/" + ref.WorkspaceID + "/" + ref.SessionID + "#" + secret, nil
}

// Only these embedded resources are public. No filesystem or data lookup occurs.
func (s *Service) browserAsset(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	parts := strings.Split(path, "/")
	page := len(parts) == 4 && parts[1] == "view" && validToken(parts[2]) && validToken(parts[3])
	asset := path == "/assets/app.css" || path == "/assets/app.js"
	if !page && !asset {
		return false
	}
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeError(w, failure("invalid_input", "body-free GET required"))
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("X-A-Stack-Instance", s.Discovery.InstanceID)
	if page {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = browserTemplate.Execute(w, s.Discovery.InstanceID)
	} else {
		name, contentType := "browser/app.js", "text/javascript; charset=utf-8"
		if path == "/assets/app.css" {
			name, contentType = "browser/app.css", "text/css; charset=utf-8"
		}
		b, _ := browserFiles.ReadFile(name)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
	return true
}
func (s *Service) browserHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/browser/open" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	var q BrowserOpenRequest
	if err := requestJSON(w, r, &q); err != nil {
		writeError(w, err)
		return true
	}
	data, err := s.Store.OpenBrowser(r.Context(), token, q)
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
func snapshotConditional(w http.ResponseWriter, r *http.Request, data any) bool {
	snapshot, ok := data.(SessionSnapshot)
	if !ok {
		return false
	}
	tag := fmt.Sprintf(`"%s:%d"`, snapshot.Session.ID, snapshot.Session.ReadRevision)
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
