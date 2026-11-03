package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/scan"
	"testing"
	"time"
)

func TestPersistentAutoRestartUsesDurableChangesNotWholeRoot(t *testing.T) {
	users := newUserFixture(t)
	id := seedReader(t, users.DB, "incremental-admin", "test-password", true, true)
	root := t.TempDir()
	leaf := filepath.Join(root, "changed-collection")
	if e := os.Mkdir(leaf, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(leaf, "new.txt")
	os.WriteFile(file, []byte("new source"), 0600)
	svc := scan.NewService(users.DB, map[string]bool{"txt": true})
	if e := svc.EnsureDirectoryQueue(); e != nil {
		t.Fatal(e)
	}
	if e := svc.RememberDirectoryChanges([]string{file}, nil); e != nil {
		t.Fatal(e)
	}
	settings := filepath.Join(t.TempDir(), "auto.py")
	if e := SaveSettings(settings, map[string]any{"scan_upload_path": root, "import_allowed_roots": []string{root}, "import_mode": "index", "import_auto_watch_enabled": true, "import_watch_user_id": id}); e != nil {
		t.Fatal(e)
	}
	for restart := 0; restart < 2; restart++ {
		called := make(chan string, 2)
		opts := ServeOptions{PersistentScan: true, SettingsPath: settings, ImportDir: importdir.Config{AllowedRoots: []string{root}}, ScanService: func() *scan.Service { return svc }, StartScan: func(string, int) error { return nil }, StartImport: func([]string, int64, bool, string) error { return nil }, StartScanBatchContext: func(_ context.Context, path string, _ int, after func() error) error { called <- path; return after() }}
		_, stop := attachImportWorker(context.Background(), opts, users.DB)
		select {
		case path := <-called:
			if path != leaf {
				stop()
				t.Fatalf("restart %d traversed %q instead of changed subtree %q", restart, path, leaf)
			}
		case <-time.After(5 * time.Second):
			stop()
			t.Fatal("durable event was not resumed")
		}
		stop()
	}
}
