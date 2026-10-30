package httpapi

import (
	"context"
	"os"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
	"testing"
)

// TestTrustedLiveAutomaticOpenLibrary is opt-in and creates its own database.
// Run only with BOOKHARBOR_LIVE_METADATA=1 and direct Open Library access.
func TestTrustedLiveAutomaticOpenLibrary(t *testing.T) {
	if os.Getenv("BOOKHARBOR_LIVE_METADATA") != "1" {
		t.Skip("isolated live acceptance only")
	}
	w, _, file := referenceFixture(t)
	w.registry = pluginruntime.NewPluginRegistry()
	if e := pluginbuiltin.Register(w.registry); e != nil {
		t.Fatal(e)
	}
	w.settings["auto_fill_meta"] = true
	if _, e := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); e != nil {
		t.Fatal(e)
	}
	w.db.Exec(`UPDATE plugin_installations SET enabled=1,status='active'`)
	w.db.Exec(`UPDATE plugin_connections SET enabled=1`)
	runtime := pluginruntime.NewRuntime(w.db, w.settings, w.registry)
	user := int64(1)
	connections, e := runtime.ConnectionsFor(metadataLookupCapability, &user)
	if e != nil {
		t.Fatal(e)
	}
	selected := false
	for _, connection := range connections {
		key, e := runtime.PluginKeyOf(connection)
		if e != nil {
			t.Fatal(e)
		}
		if key != "talebook.combo.open-library" {
			w.db.Exec(`UPDATE plugin_connections SET enabled=0 WHERE id=?`, connection.ID)
		} else {
			selected = true
		}
	}
	if !selected {
		t.Fatal("Open Library connection missing")
	}
	w.libraryDB.Exec(`UPDATE books SET title='Pride and Prejudice' WHERE id=1`)
	if e = os.WriteFile(file, []byte("Title: Pride and Prejudice\nAuthor: Jane Austen\nISBN: 9780141439518\n"), 0600); e != nil {
		t.Fatal(e)
	}
	worker := testMetadataWorker(t, w)
	if e = worker.local(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	before, e := bookGetOne(w.Config(""), 1)
	if e != nil || before.Publisher != "" || before.ISBN != "9780141439518" {
		t.Fatal(before, e)
	}
	if e = worker.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	after, e := bookGetOne(w.Config(""), 1)
	if e != nil || after.Publisher == "" {
		t.Fatal("live worker did not fill publisher", after, e)
	}
	var origin string
	if e = w.libraryDB.QueryRow(`SELECT origin FROM metadata_enrichment_writes WHERE book_id=1 AND field='publisher'`).Scan(&origin); e != nil || origin != "network:talebook.combo.open-library" {
		t.Fatal(origin, e)
	}
	t.Logf("real Open Library ISBN=%s publisher=%s audit=%s; production data untouched", after.ISBN, after.Publisher, origin)
}
