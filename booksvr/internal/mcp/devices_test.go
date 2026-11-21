package mcp

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"talebook/booksvr/internal/userdb"
)

func TestDeviceUpdateRetriesSQLiteBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE devices(id INTEGER PRIMARY KEY,reader_id INTEGER,name TEXT,device_type TEXT,ip TEXT,port INTEGER,schema TEXT,mailbox TEXT,create_time TEXT,update_time TEXT);
		CREATE TABLE audit(id INTEGER PRIMARY KEY);
		INSERT INTO devices(id,reader_id,name,device_type,ip,port,schema,mailbox,create_time,update_time) VALUES(1,1,'Before','kindle','',0,'http','reader@example.com','2026-01-01','2026-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, db, nil, nil)
	previous, err := service.device(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	competing, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer competing.Close()
	transaction, err := competing.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transaction.Exec(`INSERT INTO audit(id) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	op := operation{Arguments: map[string]any{"action": "update", "device_id": 1, "name": "After", "device_type": "kindle", "mailbox": "reader@example.com"}, Summary: map[string]any{"previous": previous}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, callErr := service.changeDevice(ctx, Principal{UserID: 1}, op)
		done <- callErr
	}()
	time.Sleep(100 * time.Millisecond)
	if err = transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("update did not survive competing audit write: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("update did not finish")
	}
	devices, err := userdb.ListDevices(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Name != "After" {
		t.Fatalf("unexpected devices: %#v", devices)
	}
}
