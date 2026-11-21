package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"reflect"
	"strings"
	"time"

	"talebook/booksvr/internal/userdb"
)

func (s *Service) devices(principal Principal) (any, error) {
	devices, err := userdb.ListDevices(s.DB, principal.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"devices": devices}, nil
}

func (s *Service) device(userID, id int64) (*userdb.Device, error) {
	devices, err := userdb.ListDevices(s.DB, userID)
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, toolError("device.not_found", "设备不存在或不属于当前读者")
}

func deviceInput(args map[string]any) (userdb.DeviceInput, error) {
	d := userdb.DeviceInput{Name: stringArg(args["name"]), DeviceType: strings.ToLower(stringArg(args["device_type"])), IP: stringArg(args["ip"]), Port: intArg(args["port"], 0), Schema: strings.ToLower(stringArg(args["schema"])), Mailbox: stringArg(args["mailbox"])}
	if d.Schema == "" {
		d.Schema = "http"
	}
	if d.Name == "" || len(d.Name) > 200 || d.DeviceType == "" || len(d.DeviceType) > 40 || (d.Schema != "http" && d.Schema != "https") {
		return d, toolError("mcp.invalid_params", "设备名称、类型或协议无效")
	}
	if d.DeviceType == "kindle" {
		addr, err := mail.ParseAddress(d.Mailbox)
		if err != nil || addr.Address != d.Mailbox {
			return d, toolError("mcp.invalid_params", "Kindle 邮箱无效")
		}
	} else if d.IP == "" || len(d.IP) > 253 || strings.ContainsAny(d.IP, "/:@?# ") || d.Port < 1 || d.Port > 65535 {
		return d, toolError("mcp.invalid_params", "设备地址或端口无效")
	}
	return d, nil
}

func deviceTarget(d *userdb.Device) string {
	if strings.EqualFold(d.DeviceType, "kindle") {
		return d.Mailbox
	}
	return (&url.URL{Scheme: d.Schema, Host: net.JoinHostPort(d.IP, fmt.Sprint(d.Port))}).String()
}

// changeDevice checks the preview snapshot inside the same write transaction.
func (s *Service) changeDevice(ctx context.Context, p Principal, op operation) (map[string]any, error) {
	var result map[string]any
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		result, err = s.changeDeviceOnce(ctx, p, op)
		if err == nil || !isSQLiteBusy(err) {
			return result, err
		}
		if attempt == 4 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "sqlite_busy")
}

func (s *Service) changeDeviceOnce(ctx context.Context, p Principal, op operation) (map[string]any, error) {
	action := stringArg(op.Arguments["action"])
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := int64(intArg(op.Arguments["device_id"], 0))
	if action != "add" {
		var current userdb.Device
		err = tx.QueryRowContext(ctx, `SELECT id,reader_id,coalesce(name,''),coalesce(device_type,''),coalesce(ip,''),coalesce(port,0),coalesce(schema,''),coalesce(mailbox,''),coalesce(create_time,''),coalesce(update_time,'') FROM devices WHERE id=? AND reader_id=?`, id, p.UserID).Scan(&current.ID, &current.ReaderID, &current.Name, &current.DeviceType, &current.IP, &current.Port, &current.Schema, &current.Mailbox, &current.CreateTime, &current.UpdateTime)
		if err == sql.ErrNoRows {
			return nil, toolError("device.not_found", "设备不存在或不属于当前读者")
		}
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(&current, op.Summary["previous"]) {
			return nil, toolError("device.changed", "设备已变化，请重新预检")
		}
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	if action == "delete" {
		_, err = tx.ExecContext(ctx, `DELETE FROM devices WHERE id=? AND reader_id=?`, id, p.UserID)
	} else {
		var d userdb.DeviceInput
		d, err = deviceInput(op.Arguments)
		if err != nil {
			return nil, err
		}
		if action == "add" {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `INSERT INTO devices(reader_id,name,device_type,ip,port,schema,mailbox,create_time,update_time) VALUES(?,?,?,?,?,?,?,?,?)`, p.UserID, d.Name, d.DeviceType, d.IP, d.Port, d.Schema, d.Mailbox, now, now)
			if err == nil {
				id, err = result.LastInsertId()
			}
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE devices SET name=?,device_type=?,ip=?,port=?,schema=?,mailbox=?,update_time=? WHERE id=? AND reader_id=?`, d.Name, d.DeviceType, d.IP, d.Port, d.Schema, d.Mailbox, now, id, p.UserID)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"device_id": id, "action": action}, nil
}
