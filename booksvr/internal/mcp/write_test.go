package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginproto"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

type fakePush struct {
	mu      sync.Mutex
	calls   int
	content string
}

func (p *fakePush) Manifest() map[string]any {
	return map[string]any{
		"protocol_version": pluginproto.ProtocolVersion, "id": "talebook.push.fake", "name": "Fake", "version": "1.0.0", "categories": []any{"integrations"}, "capabilities": []any{"integrations.push"}, "runtime_kind": "builtin", "actions": []any{"run"}, "auth_schema": map[string]any{"type": "object", "properties": map[string]any{}}, "config_schema": map[string]any{"type": "object", "properties": map[string]any{}}, "connection_owners": []any{"user"}, "permissions": []any{"books.read"}, "data_policy": map[string]any{}, "compatibility": map[string]any{"talebook": ">=0.1.0"}, "homepage": "https://example.com", "license": "GPL-3.0", "ui": map[string]any{"device_type": "kindle"},
	}
}
func (p *fakePush) DefaultPort() int { return 25 }
func (p *fakePush) Push(bookFile any, target any, context any) (any, error) {
	body, err := os.ReadFile(bookFile.(string))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.calls++
	p.content = string(body)
	p.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

func TestBookSendConfirmationIdempotencyAndTaskOwnership(t *testing.T) {
	db, service, principal := fixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(path, []byte("epub-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,'EPUB',?,12,1)`, path); err != nil {
		t.Fatal(err)
	}
	push := &fakePush{}
	registry := pluginruntime.NewPluginRegistry()
	if _, err := registry.Register(push, structCapabilities()); err != nil {
		t.Fatal(err)
	}
	service.Registry = registry
	principal.AllowWrite = true
	principal.Scopes = append(principal.Scopes, "books.write")
	prepared := call(t, service, principal, "book_send", map[string]any{"book_id": 1, "device_type": "kindle", "target": "reader@example.com", "phase": "prepare"})
	if prepared["confirmation_required"] != true {
		t.Fatal(prepared)
	}
	if push.calls != 0 {
		t.Fatal("prepare caused a push")
	}
	arguments := map[string]any{"operation_id": prepared["operation_id"], "phase": "execute", "confirm": true, "idempotency_key": "request-0001"}
	started := call(t, service, principal, "book_send", arguments)
	taskID := started["task_id"].(string)
	var final map[string]any
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		final = call(t, service, principal, "task_status", map[string]any{"task_id": taskID})
		if final["status"] == "completed" {
			break
		}
		if final["status"] == "failed" {
			t.Fatal(final)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final["status"] != "completed" {
		t.Fatalf("task did not finish: %v", final)
	}
	duplicate := call(t, service, principal, "book_send", arguments)
	if duplicate["task_id"] != taskID || duplicate["duplicate"] != true {
		t.Fatalf("idempotency: %v", duplicate)
	}
	push.mu.Lock()
	calls := push.calls
	content := push.content
	push.mu.Unlock()
	if calls != 1 || content != "epub-content" {
		t.Fatalf("calls=%d content=%q", calls, content)
	}
	other := principal
	other.UserID = 2
	raw, _ := json.Marshal(map[string]any{"name": "task_status", "arguments": map[string]any{"task_id": taskID}})
	response := service.Handle(context.Background(), other, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: raw})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("other user read task")
	}
}

func structCapabilities() pluginiface.CapabilitySet {
	return pluginiface.CapabilitySet{Interfaces: map[string]bool{"PushProvider": true}}
}

type fakeSource struct{}

func (p *fakeSource) Manifest() map[string]any {
	return map[string]any{
		"protocol_version": pluginproto.ProtocolVersion, "id": "talebook.source.fake", "name": "Fake source", "version": "1.0.0", "categories": []any{"sources"}, "capabilities": []any{"sources.acquire"}, "runtime_kind": "builtin", "actions": []any{"run"}, "auth_schema": map[string]any{"type": "object", "properties": map[string]any{}}, "config_schema": map[string]any{"type": "object", "properties": map[string]any{}}, "connection_owners": []any{"user"}, "permissions": []any{"books.read"}, "data_policy": map[string]any{}, "compatibility": map[string]any{"talebook": ">=0.1.0"}, "homepage": "https://example.com", "license": "GPL-3.0", "download_mode": "single_book",
	}
}
func (p *fakeSource) DownloadMode() pluginiface.DownloadMode { return pluginiface.DownloadSingleBook }
func (p *fakeSource) Search(string, map[string]any, map[string]any) (plugindomain.Page, error) {
	return plugindomain.Page{}, nil
}
func (p *fakeSource) Browse(string, map[string]any, map[string]any) (plugindomain.Page, error) {
	return plugindomain.Page{}, nil
}
func (p *fakeSource) GetCategories(map[string]any) ([]plugindomain.Category, error) { return nil, nil }
func (p *fakeSource) GetBook(string, map[string]any) (plugindomain.SourceBookDetail, error) {
	return plugindomain.SourceBookDetail{}, nil
}
func (p *fakeSource) Download(plugindomain.SourceBookDetail, map[string]any) (plugindomain.BookFile, error) {
	return plugindomain.BookFile{}, nil
}
func (p *fakeSource) GetTOC(plugindomain.SourceBookDetail, map[string]any) ([]plugindomain.SourceChapter, error) {
	return nil, nil
}
func (p *fakeSource) GetChapter(plugindomain.SourceChapter, map[string]any) (plugindomain.SourceContent, error) {
	return plugindomain.SourceContent{}, nil
}
func (p *fakeSource) SelfCheck(map[string]any) (plugindomain.CheckReport, error) {
	return plugindomain.CheckReport{Healthy: true}, nil
}

func TestBookImportUsesOwnedSourceAndReportsTask(t *testing.T) {
	db, service, principal := fixture(t)
	registry := pluginruntime.NewPluginRegistry()
	provider := &fakeSource{}
	caps := pluginiface.CapabilitySet{Interfaces: map[string]bool{"SourceProvider": true}, DownloadMode: pluginiface.DownloadSingleBook, HasSelfCheck: true, Methods: map[string]bool{"download": true}}
	if _, err := registry.Register(provider, caps); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"PLUGIN_SECRET_KEY": "import-test"}
	service.Settings = settings
	service.Registry = registry
	if _, err := pluginruntime.EnsureBuiltinInstallations(db, 1, settings, registry); err != nil {
		t.Fatal(err)
	}
	installation, err := userdb.GetPluginInstallationByKey(db, "talebook.source.fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE plugin_installations SET enabled=1,status='active' WHERE id=?`, installation.ID); err != nil {
		t.Fatal(err)
	}
	connection, err := pluginruntime.NewRuntime(db, settings, registry).SaveConnection(installation.ID, "user", 1, nil, "source", map[string]any{}, nil, "", "source")
	if err != nil {
		t.Fatal(err)
	}
	var imports atomic.Int32
	service.Import = func(ctx context.Context, p mcp.Principal, sourceKey, bookURL, format string, clean bool, taskID int64) error {
		imports.Add(1)
		if p.UserID != 1 || sourceKey != fmt.Sprintf("plugin:%d", connection.ID) || bookURL != "external-book" || format != "EPUB" || !clean {
			t.Fatalf("unexpected import arguments")
		}
		service.Background.UpdateProgress(taskID, 100, map[string]any{"done": 1, "total": 1, "book_id": int64(3)}, "")
		return nil
	}
	principal.AllowWrite = true
	principal.Scopes = append(principal.Scopes, "books.write")
	sourceKey := fmt.Sprintf("plugin:%d", connection.ID)
	prepared := call(t, service, principal, "book_import", map[string]any{"source_key": sourceKey, "book_url": "external-book", "format": "EPUB", "clean": true, "phase": "prepare"})
	if imports.Load() != 0 {
		t.Fatal("prepare imported a book")
	}
	started := call(t, service, principal, "book_import", map[string]any{"operation_id": prepared["operation_id"], "phase": "execute", "confirm": true, "idempotency_key": "import-0001"})
	var final map[string]any
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		final = call(t, service, principal, "task_status", map[string]any{"task_id": started["task_id"]})
		if final["status"] == "completed" {
			break
		}
		if final["status"] == "failed" {
			t.Fatal(final)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final["status"] != "completed" || final["book_id"] != int64(3) || imports.Load() != 1 {
		t.Fatalf("import result=%v calls=%d", final, imports.Load())
	}
}

func TestBookUpdateConsumesConfirmationOnce(t *testing.T) {
	_, service, principal := fixture(t)
	principal.AllowWrite = true
	principal.Scopes = append(principal.Scopes, "books.write")
	service.EditMetadata = func(context.Context, mcp.Principal, int64, map[string]any, bool) error { return nil }
	prepared := call(t, service, principal, "book_update", map[string]any{"book_id": 1, "title": "New title"})
	var checks, edits atomic.Int32
	both := make(chan struct{})
	service.EditMetadata = func(ctx context.Context, _ mcp.Principal, _ int64, patch map[string]any, apply bool) error {
		if apply {
			if patch["title"] != "New title" {
				return fmt.Errorf("unexpected patch: %v", patch)
			}
			edits.Add(1)
			return nil
		}
		if checks.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan *mcp.Response, 2)
	for _, key := range []string{"review-confirm-1", "review-confirm-2"} {
		args := map[string]any{"operation_id": prepared["operation_id"], "phase": "execute", "confirm": true, "idempotency_key": key}
		params, _ := json.Marshal(map[string]any{"name": "book_update", "arguments": args})
		go func() {
			results <- service.Handle(ctx, principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
		}()
	}
	var taskID string
	for range 2 {
		result := (<-results).Result.(map[string]any)
		if result["isError"] == false {
			if taskID != "" {
				t.Fatal("confirmation queued a second write")
			}
			taskID = result["structuredContent"].(map[string]any)["task_id"].(string)
		}
	}
	if taskID == "" {
		t.Fatal("confirmation did not queue a write")
	}
	for ctx.Err() == nil {
		result := call(t, service, principal, "task_status", map[string]any{"task_id": taskID})
		if result["status"] == "completed" {
			if edits.Load() != 1 {
				t.Fatalf("applied %d edits", edits.Load())
			}
			return
		}
		if result["status"] == "failed" {
			t.Fatal(result)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("write did not finish")
}

func TestDeviceManagementAndSelectedSend(t *testing.T) {
	db, service, principal := fixture(t)
	principal.AllowWrite = true
	principal.Scopes = append(principal.Scopes, "books.write")
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(path, []byte("epub-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,'EPUB',?,12,1)`, path); err != nil {
		t.Fatal(err)
	}
	push := &fakePush{}
	registry := pluginruntime.NewPluginRegistry()
	if _, err := registry.Register(push, structCapabilities()); err != nil {
		t.Fatal(err)
	}
	service.Registry = registry
	run := func(name string, args map[string]any) map[string]any {
		preview := call(t, service, principal, name, args)
		result := call(t, service, principal, name, map[string]any{"phase": "execute", "confirm": true, "operation_id": preview["operation_id"], "idempotency_key": fmt.Sprintf("device-test-%d", time.Now().UnixNano())})
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			state := call(t, service, principal, "task_status", map[string]any{"task_id": result["task_id"]})
			if state["status"] == "completed" {
				return state
			}
			if state["status"] == "failed" {
				t.Fatal(state)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("task timed out")
		return nil
	}
	added := run("device_manage", map[string]any{"action": "add", "name": "My Kindle", "device_type": "kindle", "mailbox": "reader@example.com"})
	id := added["result"].(map[string]any)["device_id"].(int64)
	devices := call(t, service, principal, "device_list", nil)["devices"].([]*userdb.Device)
	if len(devices) != 1 || devices[0].ID != id {
		t.Fatal(devices)
	}
	other := principal
	other.UserID = 2
	params, _ := json.Marshal(map[string]any{"name": "book_send", "arguments": map[string]any{"book_id": 1, "device_id": id}})
	denied := service.Handle(context.Background(), other, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
	if denied.Result.(map[string]any)["isError"] != true {
		t.Fatal("other user's device accessible")
	}
	preview := call(t, service, principal, "book_send", map[string]any{"book_id": 1, "device_id": id, "format": "EPUB"})
	if _, err := db.Exec(`UPDATE devices SET mailbox='changed@example.com' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	rejectedParams, _ := json.Marshal(map[string]any{"name": "book_send", "arguments": map[string]any{"phase": "execute", "confirm": true, "operation_id": preview["operation_id"], "idempotency_key": "changed-device"}})
	rejected := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: rejectedParams})
	if rejected.Result.(map[string]any)["isError"] != true {
		t.Fatal("changed destination accepted")
	}
	run("book_send", map[string]any{"book_id": 1, "device_id": id, "format": "EPUB"})
	run("device_manage", map[string]any{"action": "update", "device_id": id, "name": "Renamed", "device_type": "kindle", "mailbox": "reader@example.com"})
	run("device_manage", map[string]any{"action": "delete", "device_id": id})
	if list := call(t, service, principal, "device_list", nil)["devices"].([]*userdb.Device); len(list) != 0 {
		t.Fatal(list)
	}
}
