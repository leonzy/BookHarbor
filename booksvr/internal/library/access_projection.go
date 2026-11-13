package library

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"talebook/booksvr/internal/access"
)

// AccessBookSnapshot must be read from a coherent library/source snapshot.
// Sources must use the exact same selector as resource serving, including ZIP
// member identity and inherited conversion origins; a path alone is not a key.
type AccessBookSnapshot struct {
	ID               int64
	Title, Timestamp string
	Files            []AccessFileSnapshot
}

type AccessFileSnapshot struct {
	ID, Format    string
	Size, Version int64
	Facts         access.FileFacts
}

// BuildAccessProfiles persists a bounded set of distinct compiled profiles.
// Keeping even unrestricted mappings here makes the switch atomic. The runtime
// can preserve the old query fast path when unrestricted is proven separately.
func (s AccessStore) BuildAccessProfiles(ctx context.Context, t AccessBuildToken, principals []string) (map[string]access.Compiled, error) {
	r, err := s.Revision(ctx, t.Revision)
	if err != nil {
		return nil, err
	}
	if r.Version != t.Version || r.Status != "preparing" {
		return nil, ErrAccessConflict
	}
	compiled := map[string]access.Compiled{}
	// Reserve the shared unassigned-user profile so a new account does not
	// require another whole-library build or inherit guest-specific ceilings.
	defaultProfile := r.Policy.Compile("", false)
	compiled[defaultProfile.Key()] = defaultProfile
	mapping := map[string]string{}
	for _, id := range principals {
		c := r.Policy.Compile(id, false)
		compiled[c.Key()] = c
		mapping[id] = c.Key()
	}
	if len(compiled) > 16 {
		return nil, fmt.Errorf("access.profile_budget: 超过 16 个不同权限组合，当前规则保持不变")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return nil, err
	}
	if err = accessBuildCurrent(ctx, tx, t); err != nil {
		return nil, err
	}
	known, err := accessPrincipals(ctx, tx)
	if err != nil {
		return nil, err
	}
	if len(mapping) != len(known) {
		return nil, ErrAccessConflict
	}
	for id := range known {
		if _, ok := mapping[id]; !ok {
			return nil, ErrAccessConflict
		}
	}
	for _, table := range []string{"acl_profile_file", "acl_profile_book", "acl_profile_facet", "acl_facet_count", "acl_profile_count", "acl_profile_principal", "acl_profile"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE revision=?`, t.Revision); err != nil {
			return nil, err
		}
	}
	for key, c := range compiled {
		if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile(revision,profile,unrestricted) VALUES(?,?,?)`, t.Revision, key, c.Unrestricted()); err != nil {
			return nil, err
		}
	}
	for id, key := range mapping {
		if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile_principal(revision,principal,profile) VALUES(?,?,?)`, t.Revision, id, key); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return compiled, nil
}

// WriteAccessBatch only writes a short, bounded set of source snapshots. The
// caller must yield between batches and re-read changed source versions.
func (s AccessStore) WriteAccessBatch(ctx context.Context, t AccessBuildToken, profiles map[string]access.Compiled, books []AccessBookSnapshot) error {
	revision, err := s.Revision(ctx, t.Revision)
	if err != nil {
		return err
	}
	if len(books) > 64 || len(profiles) > 16 {
		return fmt.Errorf("access batch exceeds budget")
	}
	nfiles := 0
	seen := map[int64]bool{}
	for _, b := range books {
		if b.ID <= 0 || seen[b.ID] {
			return fmt.Errorf("invalid or duplicate book")
		}
		seen[b.ID] = true
		nfiles += len(b.Files)
		files := map[string]bool{}
		owner := ""
		haveOwner := false
		for _, f := range b.Files {
			if f.ID == "" || files[f.ID] || f.Size < 0 || f.Version < 1 || f.Format == "" {
				return fmt.Errorf("invalid source snapshot")
			}
			files[f.ID] = true
			fo := ""
			if f.Facts.Private {
				fo = f.Facts.PrivateOwner
				if fo == "" {
					return fmt.Errorf("private source has no stable owner")
				}
			}
			if haveOwner && owner != fo {
				return fmt.Errorf("book privacy inconsistent across formats")
			}
			owner = fo
			haveOwner = true
		}
	}
	if nfiles > 512 {
		return fmt.Errorf("access file batch exceeds budget")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return err
	}
	if err = accessProjectionCurrent(ctx, tx, t); err != nil {
		return err
	}
	for index := range books {
		b := &books[index]
		var changed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_dirty WHERE book_id=? AND version>?)`, b.ID, t.Watermark).Scan(&changed); err != nil {
			return err
		}
		if changed {
			return ErrAccessNotReady
		}
		classes, e := accessBookClasses(ctx, tx, revision, b.ID)
		if e != nil {
			return e
		}
		for i := range b.Files {
			b.Files[i].Facts.Classes = append(b.Files[i].Facts.Classes, classes...)
		}
		for _, f := range b.Files {
			if _, err = tx.ExecContext(ctx, `DELETE FROM acl_source_rule WHERE file_id=? AND revision=?`, f.ID, t.Revision); err != nil {
				return err
			}
			for _, rule := range revision.Policy.MatchingOriginRules(t.Revision, t.Version, f.Facts) {
				if _, err = tx.ExecContext(ctx, `INSERT INTO acl_source_rule(file_id,revision,rule_id) VALUES(?,?,?)`, f.ID, t.Revision, rule); err != nil {
					return err
				}
			}
		}
	}
	roots := map[string]string{}
	rootRows, e := tx.QueryContext(ctx, `SELECT id,path FROM acl_root`)
	if e != nil {
		return e
	}
	for rootRows.Next() {
		var id, p string
		if e = rootRows.Scan(&id, &p); e != nil {
			rootRows.Close()
			return e
		}
		roots[id] = filepath.ToSlash(p)
	}
	e = rootRows.Err()
	rootRows.Close()
	if e != nil {
		return e
	}
	for key, c := range profiles {
		if key != c.Key() {
			return fmt.Errorf("profile key mismatch")
		}
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM acl_profile WHERE revision=? AND profile=?`, t.Revision, key).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return ErrAccessConflict
		}
		for _, b := range books {
			if err = removeAccessBook(ctx, tx, t.Revision, key, b.ID); err != nil {
				return err
			}
			// A derived private source makes the shared book metadata private too.
			// Resolve this before iterating formats so ordering cannot change access.
			bookOwner, consistent := accessBookOwnership(b.Files)
			if !consistent {
				continue
			}
			var maxSize int64
			count := 0
			owner := ""
			locations := map[string]bool{}
			for _, f := range b.Files {
				facts := f.Facts
				fileOwner := ""
				var valid bool
				facts, valid = accessOwnershipFacts(facts, &fileOwner, 0)
				if !valid {
					continue
				}
				// Shared projections contain the resources satisfying this profile,
				// with ownership stored separately. Every reader query must include
				// owner IN ('', authenticated principal), never just book membership.
				if !c.Decide(facts).Allowed {
					continue
				}
				owner = bookOwner
				physical := ""
				if root := roots[facts.Root]; root != "" {
					physical = path.Join(root, strings.TrimPrefix(facts.Directory, "/"))
					locations[physical] = true
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile_file(revision,profile,file_id,book_id,format,source_root,directory,physical_directory,size,source_version,owner) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, t.Revision, key, f.ID, b.ID, strings.ToUpper(f.Format), facts.Root, facts.Directory, physical, f.Size, f.Version, owner); err != nil {
					return err
				}
				count++
				if f.Size > maxSize {
					maxSize = f.Size
				}
			}
			if count == 0 {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile_book(revision,profile,book_id,title,timestamp,size,owner) VALUES(?,?,?,?,?,?,?)`, t.Revision, key, b.ID, b.Title, b.Timestamp, maxSize, owner); err != nil {
				return err
			}
			if err = addAccessDirectories(ctx, tx, t.Revision, key, b.ID, owner, locations); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile_count(revision,profile,owner,books,files) VALUES(?,?,?,1,?) ON CONFLICT(revision,profile,owner) DO UPDATE SET books=books+1,files=files+excluded.files`, t.Revision, key, owner, count); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func removeAccessBook(ctx context.Context, tx *sql.Tx, revision int64, profile string, id int64) error {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT owner FROM acl_profile_book WHERE revision=? AND profile=? AND book_id=?`, revision, profile, id).Scan(&owner)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE acl_profile_count SET books=books-1,files=files-(SELECT count(*) FROM acl_profile_file WHERE revision=? AND profile=? AND book_id=?) WHERE revision=? AND profile=? AND owner=?`, revision, profile, id, revision, profile, owner); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM acl_profile_file WHERE revision=? AND profile=? AND book_id=?`, revision, profile, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM acl_profile_book WHERE revision=? AND profile=? AND book_id=?`, revision, profile, id)
	return err
}
