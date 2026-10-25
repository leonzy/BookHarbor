package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/scan"
	"testing"
	"time"
)

func TestAutoImportIncrementalDoesNotRepeatAndRetainsEvents(t *testing.T) {
	fx := newUserFixture(t)
	id := seedReader(t, fx.DB, "delta-admin", "test", true, true)
	root := t.TempDir()
	settings := filepath.Join(t.TempDir(), "auto.py")
	SaveSettings(settings, map[string]any{"import_auto_watch_enabled": true, "import_watch_user_id": id, "import_allowed_roots": []string{root}, "import_watch_interval_seconds": 5, "import_mode": "index"})
	service := scan.NewService(scanTestDB(t), map[string]bool{"txt": true})
	scanned := make(chan string, 10)
	release := make(chan struct{})
	opts := ServeOptions{SettingsPath: settings, ScanService: func() *scan.Service { return service }, StartImport: func([]string, int64, bool, string) error { return nil }, StartScan: func(p string, _ int) error {
		scanned <- p
		if p == root {
			<-release
		}
		return nil
	}}
	running, stop := attachImportWorker(context.Background(), opts, fx.DB)
	defer stop()
	select {
	case p := <-scanned:
		if p != root {
			t.Fatal(p)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("no initial scan")
	}
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	os.WriteFile(a, []byte("a"), 0600)
	os.WriteFile(b, []byte("b"), 0600)
	running.QueueSourcePaths([]string{a, a}, false)
	running.QueueSourcePaths([]string{b}, false)
	close(release)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case p := <-scanned:
			if p != a && p != b {
				t.Fatal("full scan repeated", p)
			}
			if seen[p] {
				t.Fatal("event not coalesced", p)
			}
			seen[p] = true
		case <-time.After(9 * time.Second):
			t.Fatal("busy events lost", seen)
		}
	}
	select {
	case p := <-scanned:
		t.Fatal("idle scan repeated", p)
	case <-time.After(6 * time.Second):
	}
}

func TestAutoImportOfflineRootDoesNotRepeatHealthyRoot(t *testing.T) {
	fx := newUserFixture(t)
	id := seedReader(t, fx.DB, "offline-admin", "test", true, true)
	root := t.TempDir()
	offline := filepath.Join(t.TempDir(), "absent")
	settings := filepath.Join(t.TempDir(), "auto.py")
	SaveSettings(settings, map[string]any{"import_auto_watch_enabled": true, "import_watch_user_id": id, "import_allowed_roots": []string{root, offline}, "import_watch_interval_seconds": 5})
	service := scan.NewService(scanTestDB(t), nil)
	calls := make(chan string, 10)
	_, stop := attachImportWorker(context.Background(), ServeOptions{SettingsPath: settings, ScanService: func() *scan.Service { return service }, StartScan: func(p string, _ int) error { calls <- p; return nil }, StartImport: func([]string, int64, bool, string) error { return nil }}, fx.DB)
	defer stop()
	select {
	case p := <-calls:
		if p != root {
			t.Fatal(p)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("initial scan missing")
	}
	select {
	case p := <-calls:
		t.Fatal("offline root caused full retry", p)
	case <-time.After(7 * time.Second):
	}
}
