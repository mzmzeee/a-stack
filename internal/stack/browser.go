package stack

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed browser/*
var browserFiles embed.FS
var browserTemplate = template.Must(template.ParseFS(browserFiles, "browser/index.html"))

// browserAssetTags holds an ETag per embedded asset: a hash of its content, so
// a browser revalidates and downloads the file again only after it changed.
// browserAssets names both files together. The page has it, and after a
// restart it compares it with the one of the service, to offer a reload.
var browserAssetTags, browserAssets = func() (map[string]string, string) {
	tags, all := map[string]string{}, sha256.New()
	for _, name := range []string{"browser/app.js", "browser/app.css"} {
		b, _ := browserFiles.ReadFile(name)
		sum := sha256.Sum256(b)
		tags[name] = `"` + hex.EncodeToString(sum[:12]) + `"`
		all.Write(sum[:])
	}
	return tags, hex.EncodeToString(all.Sum(nil)[:12])
}()

// ViewLinkAge is how long the one-time code of a view link works. ViewTokenAge
// is how long the view token works that the page gets for the code.
const (
	ViewLinkAge  = time.Minute
	ViewTokenAge = 30 * 24 * time.Hour
)

// migration7 makes view links one-time and view tokens expiring. view_links
// keeps only the hash of each unused code. expires_at is a Unix time; 0 means
// that the credential does not expire. Old browser credentials are deleted,
// because their secrets are in browser histories.
const migration7 = `
ALTER TABLE capabilities ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
DELETE FROM capabilities WHERE kind='browser';
CREATE TABLE view_links(code_hash TEXT PRIMARY KEY, session_id TEXT NOT NULL, expires_at INTEGER NOT NULL) WITHOUT ROWID;
`

type BrowserOpenRequest struct {
	Context
	OperationID    string `json:"operation_id"`
	CredentialHash string `json:"credential_hash"` // of the view link's one-time code
}

// OpenBrowser lets only the Operator make a view link for one Session. The
// caller generates the code: neither receipts nor the service receive it.
// Each new link also deletes the expired links and view tokens.
func (s *Store) OpenBrowser(ctx context.Context, token string, r BrowserOpenRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "browser.open", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		if p.Kind != OperatorCapability {
			return errOperatorView
		}
		return replaySession(ctx, tx, p, receipt)
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if p.Kind != OperatorCapability {
			return Receipt{}, errOperatorView
		}
		session, err := ReadSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if !validHash(r.CredentialHash) {
			return Receipt{}, failure("invalid_input", "credential_hash must be a SHA-256 lowercase hex digest")
		}
		now := clockNow().Unix()
		if _, err = tx.ExecContext(ctx, "DELETE FROM view_links WHERE expires_at<=?", now); err != nil {
			return Receipt{}, storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM capabilities WHERE kind='browser' AND expires_at<=?", now); err != nil {
			return Receipt{}, storageError(err)
		}
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM view_links WHERE code_hash=?", r.CredentialHash).Scan(&exists); err != nil {
			return Receipt{}, storageError(err)
		}
		if exists != 0 {
			return Receipt{}, failure("conflict", "credential hash already exists")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO view_links(code_hash,session_id,expires_at) VALUES(?,?,?)", r.CredentialHash, session.ID, now+int64(ViewLinkAge/time.Second))
		if err != nil {
			return Receipt{}, storageError(err)
		}
		return Receipt{ResourceID: session.ID, Revision: session.Revision, Outcome: "view-link-issued"}, nil
	})
}

var errOperatorView = failure("forbidden", "only the Operator opens the browser view")

func (c *Client) BrowserURL(ctx context.Context, ref Context) (string, error) {
	code := opaqueID()
	_, err := c.Do(ctx, "POST", "/v1/browser/open", BrowserOpenRequest{Context: ref, OperationID: opaqueID(), CredentialHash: tokenHash(code)})
	if err != nil {
		return "", err
	}
	return c.Discovery.URL + "/view/" + ref.WorkspaceID + "/" + ref.SessionID + "#" + code, nil
}

type ViewRedeemRequest struct {
	Context
	Code string `json:"code"`
}

// errViewLink names no secret; the page shows its own text for it.
var errViewLink = failure("unauthorized", "view link expired or already used; run a-stack open for a new one")

// RedeemViewLink trades the one-time code of a view link for a view token of
// its Session. The code works once, for ViewLinkAge after it was made.
func (s *Store) RedeemViewLink(ctx context.Context, r ViewRedeemRequest) (string, error) {
	if !validToken(r.Code) {
		return "", errViewLink
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", storageError(err)
	}
	defer tx.Rollback()
	var session string
	var expires int64
	err = tx.QueryRowContext(ctx, "DELETE FROM view_links WHERE code_hash=? RETURNING session_id,expires_at", tokenHash(r.Code)).Scan(&session, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errViewLink
	}
	if err != nil {
		return "", storageError(err)
	}
	// An expired code, or one sent for another Session, is used up all the same.
	if _, err = ReadSession(ctx, tx, Principal{Kind: BrowserCapability, SessionID: session}, r.Context); err != nil || expires <= clockNow().Unix() {
		if tx.Commit() != nil {
			return "", storageError()
		}
		return "", errViewLink
	}
	token, err := issueViewToken(ctx, tx, session)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", storageError(err)
	}
	return token, nil
}

type ViewRevokeRequest struct {
	Context
	All bool `json:"all"`
}

// RevokeViews deletes the view tokens and unused view links of one Session, or
// of all Sessions, and returns the number of view tokens. Open tabs lose access
// at their next refresh.
func (s *Store) RevokeViews(ctx context.Context, token string, r ViewRevokeRequest) (int64, error) {
	if r.All == (r.WorkspaceID != "" || r.SessionID != "") {
		return 0, failure("invalid_input", "give either --all or --workspace and --session")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return 0, err
	}
	if p.Kind != OperatorCapability {
		return 0, failure("forbidden", "Operator credential required")
	}
	where, args := "", []any{}
	if !r.All {
		if _, err = ReadSession(ctx, tx, p, r.Context); err != nil {
			return 0, publicError(err)
		}
		where, args = " AND session_id=?", []any{r.SessionID}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM view_links WHERE 1"+where, args...); err != nil {
		return 0, storageError(err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM capabilities WHERE kind='browser'"+where, args...)
	if err != nil {
		return 0, storageError(err)
	}
	n, err := result.RowsAffected()
	if err != nil || tx.Commit() != nil {
		return 0, storageError()
	}
	return n, nil
}

func (c *Client) RevokeViews(ctx context.Context, r ViewRevokeRequest) (json.RawMessage, error) {
	return c.Do(ctx, "POST", "/v1/browser/revoke", r)
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
	w.Header().Set("X-A-Stack-Version", s.Version)
	w.Header().Set("X-A-Stack-Assets", browserAssets)
	if page {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = browserTemplate.Execute(w, struct{ Instance, Version, Assets string }{s.Discovery.InstanceID, s.Version, browserAssets})
	} else {
		name, contentType := "browser/app.js", "text/javascript; charset=utf-8"
		if path == "/assets/app.css" {
			name, contentType = "browser/app.css", "text/css; charset=utf-8"
		}
		// The assets hold no data or credentials, so a browser may keep them,
		// but it must ask each time whether they are still current.
		tag := browserAssetTags[name]
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
		b, _ := browserFiles.ReadFile(name)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
	return true
}
func (s *Service) browserHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/browser/open" && r.URL.Path != "/v1/browser/revoke" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	var data any
	var err error
	if r.URL.Path == "/v1/browser/revoke" {
		var q ViewRevokeRequest
		var n int64
		if err = requestJSON(w, r, &q); err == nil {
			n, err = s.Store.RevokeViews(r.Context(), token, q)
		}
		data = map[string]int64{"revoked": n}
	} else {
		var q BrowserOpenRequest
		if err = requestJSON(w, r, &q); err == nil {
			data, err = s.Store.OpenBrowser(r.Context(), token, q)
		}
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}

// redeemHTTP serves the one data request without a bearer credential: the page
// sends the code of its view link and gets a view token.
func (s *Service) redeemHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return
	}
	var q ViewRedeemRequest
	err := requestJSON(w, r, &q)
	var token string
	if err == nil {
		token, err = s.Store.RedeemViewLink(r.Context(), q)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"token": token}})
}

// snapshotTag is the ETag of a snapshot: its Session, read revision and archive
// mark.
func snapshotTag(snapshot SessionSnapshot) string {
	return fmt.Sprintf(`"%s:%d:%d"`, snapshot.Session.ID, snapshot.Session.ReadRevision, snapshot.archiveMark)
}

// knownArchiveMark reads the archive mark from the snapshot ETag that the
// view sends back. An ETag without one, from an older service, gives -1, so
// that the next answer is a full one with the new ETag.
func knownArchiveMark(r *http.Request, q *SnapshotRequest) {
	tag := r.Header.Get("If-None-Match")
	if tag == "" {
		return
	}
	q.hasKnownMark, q.knownMark = true, -1
	parts := strings.Split(strings.Trim(tag, `"`), ":")
	if len(parts) == 3 {
		if mark, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
			q.knownMark = mark
		}
	}
}
func snapshotConditional(w http.ResponseWriter, r *http.Request, data any) bool {
	snapshot, ok := data.(SessionSnapshot)
	if !ok {
		return false
	}
	tag := snapshotTag(snapshot)
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
