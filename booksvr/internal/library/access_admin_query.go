package library

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"talebook/booksvr/internal/access"
)

// AccessDirectoryTarget resolves a transient UI node to the stable source root.
// The longest registered root wins, including when it is currently offline.
func (s AccessStore) AccessDirectoryTarget(ctx context.Context, node int64, recursive bool) (*access.Target, error) {
	var path string
	if err := s.DB.QueryRowContext(ctx, `SELECT path FROM book_directory_nodes WHERE rowid=?`, node).Scan(&path); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,path FROM acl_root ORDER BY length(path) DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, root string
		if err = rows.Scan(&id, &root); err != nil {
			return nil, err
		}
		rel, e := filepath.Rel(root, path)
		if e != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		directory := "/"
		if rel != "." {
			directory += filepath.ToSlash(rel)
		}
		return &access.Target{Root: id, Directory: directory, Recursive: recursive}, nil
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrAccessNotReady
}

type AccessPrincipalInfo struct {
	ID        string `json:"id"`
	AccountID int64  `json:"account_id"`
	Guest     bool   `json:"guest"`
	Username  string `json:"username,omitempty"`
	Name      string `json:"name,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
}
type AccessOverview struct {
	Active     int64                 `json:"active"`
	Revision   *AccessRevision       `json:"revision"`
	Principals []AccessPrincipalInfo `json:"principals"`
	Roots      []AccessRoot          `json:"roots"`
}

func (s AccessStore) AccessOverview(ctx context.Context) (AccessOverview, error) {
	out := AccessOverview{Principals: []AccessPrincipalInfo{}, Roots: []AccessRoot{}}
	if err := s.DB.QueryRowContext(ctx, `SELECT active_revision FROM acl_state WHERE id=1`).Scan(&out.Active); err != nil {
		return out, err
	}
	var draft int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM acl_revision WHERE base_revision=? AND status IN ('draft','preparing','ready','failed','cancelled') ORDER BY id DESC LIMIT 1`, out.Active).Scan(&draft)
	if err == sql.ErrNoRows {
		draft = out.Active
	} else if err != nil {
		return out, err
	}
	if draft > 0 {
		r, err := s.Revision(ctx, draft)
		if err != nil {
			return out, err
		}
		out.Revision = &r
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,account_id,guest FROM acl_principal WHERE disabled=0 ORDER BY guest DESC,account_id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p AccessPrincipalInfo
		if err = rows.Scan(&p.ID, &p.AccountID, &p.Guest); err != nil {
			rows.Close()
			return out, err
		}
		out.Principals = append(out.Principals, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT id,path,ready FROM acl_root ORDER BY path`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r AccessRoot
		if err = rows.Scan(&r.ID, &r.Path, &r.Ready); err != nil {
			rows.Close()
			return out, err
		}
		out.Roots = append(out.Roots, r)
	}
	err = rows.Err()
	rows.Close()
	return out, err
}

type AccessDirectoryInfo struct {
	Root      string `json:"root"`
	Directory string `json:"directory"`
	Name      string `json:"name"`
}

func (s AccessStore) AccessDirectoryChildren(ctx context.Context, root, relative string, start int) ([]AccessDirectoryInfo, bool, error) {
	if root == "" || !strings.HasPrefix(relative, "/") || strings.Contains(relative, "\\") || filepath.ToSlash(filepath.Clean(relative)) != relative || start < 0 {
		return nil, false, fmt.Errorf("invalid directory")
	}
	var rootPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT path FROM acl_root WHERE id=?`, root).Scan(&rootPath); err != nil {
		return nil, false, err
	}
	parent := rootPath
	if relative != "/" {
		parent = filepath.Join(rootPath, filepath.FromSlash(strings.TrimPrefix(relative, "/")))
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT path,name FROM book_directory_nodes WHERE parent=? ORDER BY name,path LIMIT 101 OFFSET ?`, parent, start)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []AccessDirectoryInfo{}
	for rows.Next() {
		var path, name string
		if err = rows.Scan(&path, &name); err != nil {
			return nil, false, err
		}
		rel, e := filepath.Rel(rootPath, path)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		out = append(out, AccessDirectoryInfo{Root: root, Directory: "/" + filepath.ToSlash(rel), Name: name})
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > 100
	if more {
		out = out[:100]
	}
	return out, more, nil
}
