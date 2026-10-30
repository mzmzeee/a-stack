package stack

import (
	"context"
	"database/sql"
	"path/filepath"
)

type SessionLink struct {
	Context
	LocalWorkspaceID string `json:"local_workspace_id"`
	Path             string `json:"path"`
	Remote           string `json:"remote"`
	DatabaseID       string `json:"database_id"`
	CredentialFile   string `json:"credential_file"`
	Name             string `json:"name"`
}

type SessionLinkRequest struct {
	Link   SessionLink `json:"link"`
	Remove bool        `json:"remove,omitempty"`
}

func (s *Store) WriteSessionLink(ctx context.Context, token string, r SessionLinkRequest) (SessionLink, error) {
	link := r.Link
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return link, storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return link, err
	}

	if err = operator(p); err != nil {
		return link, err
	}

	if !validToken(link.SessionID) || !validToken(link.DatabaseID) {
		return link, failure("invalid_input", "linked Session and database required")
	}

	if r.Remove {
		_, err = tx.ExecContext(ctx, "DELETE FROM session_links WHERE database_id=? AND session_id=?", link.DatabaseID, link.SessionID)
	} else {
		if !validToken(link.WorkspaceID) || !remoteName.MatchString(link.Remote) || !filepath.IsAbs(link.CredentialFile) || len(link.Name) == 0 || len(link.Name) > 128 {
			return link, failure("invalid_input", "invalid Session link")
		}

		link.Path, err = canonicalDirectory(link.Path)
		if err != nil {
			return link, err
		}

		err = tx.QueryRowContext(ctx, "SELECT id FROM workspaces WHERE path=?", link.Path).Scan(&link.LocalWorkspaceID)
		if err == sql.ErrNoRows {
			link.LocalWorkspaceID = opaqueID()
			_, err = tx.ExecContext(ctx, "INSERT INTO workspaces(id,path,created_at) VALUES(?,?,?)", link.LocalWorkspaceID, link.Path, now())
		}

		if err != nil {
			return link, storageError(err)
		}

		_, err = tx.ExecContext(ctx, "INSERT INTO session_links(database_id,session_id,workspace_id,local_workspace_id,remote,credential_file,name) VALUES(?,?,?,?,?,?,?) ON CONFLICT(database_id,session_id) DO UPDATE SET workspace_id=excluded.workspace_id,local_workspace_id=excluded.local_workspace_id,remote=excluded.remote,credential_file=excluded.credential_file,name=excluded.name", link.DatabaseID, link.SessionID, link.WorkspaceID, link.LocalWorkspaceID, link.Remote, link.CredentialFile, link.Name)
	}

	if err == nil {
		err = tx.Commit()
	}

	return link, storageOrNil(err)
}

func (s *Store) ListSessionLinks(ctx context.Context, token string) ([]SessionLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return nil, err
	}

	if err = operator(p); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, "SELECT l.database_id,l.session_id,l.workspace_id,l.local_workspace_id,w.path,l.remote,l.credential_file,l.name FROM session_links l JOIN workspaces w ON w.id=l.local_workspace_id ORDER BY l.name,l.session_id")
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()

	links := []SessionLink{}
	for rows.Next() {
		var link SessionLink
		if err = rows.Scan(&link.DatabaseID, &link.SessionID, &link.WorkspaceID, &link.LocalWorkspaceID, &link.Path, &link.Remote, &link.CredentialFile, &link.Name); err != nil {
			return nil, storageError(err)
		}

		links = append(links, link)
	}

	return links, storageOrNil(rows.Err())
}
