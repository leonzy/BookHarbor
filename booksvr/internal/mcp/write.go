package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"talebook/booksvr/internal/background"
	"talebook/booksvr/internal/booktools"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/sourcecatalog"
	"talebook/booksvr/internal/sourcefile"
	"talebook/booksvr/internal/userdb"
)

func (s *Service) write(ctx context.Context, principal Principal, name string, args map[string]any) (any, error) {
	if !principal.AllowWrite {
		return nil, toolError("mcp.write_disabled", "此 MCP 连接未开启写操作")
	}
	phase := stringArg(args["phase"])
	if phase == "" {
		phase = "prepare"
	}
	switch phase {
	case "prepare":
		summary, err := s.validateWrite(ctx, principal, name, args)
		if err != nil {
			return nil, err
		}
		id, err := randomID("op_")
		if err != nil {
			return nil, err
		}
		copied := map[string]any{}
		for key, value := range args {
			copied[key] = value
		}
		s.mu.Lock()
		s.expireLocked()
		s.operations[id] = &operation{ID: id, Tool: name, UserID: principal.UserID, Arguments: copied, Summary: summary, Expires: time.Now().Add(10 * time.Minute)}
		s.mu.Unlock()
		return map[string]any{"phase": "prepare", "operation_id": id, "confirmation_required": true, "summary": summary, "expires_in_seconds": 600, "next": "使用 operation_id、phase=execute、confirm=true 和唯一 idempotency_key 再次调用本工具"}, nil
	case "execute":
		operationID := stringArg(args["operation_id"])
		key := stringArg(args["idempotency_key"])
		if operationID == "" || len(key) < 8 || len(key) > 128 || !boolArg(args["confirm"]) {
			return nil, toolError("mcp.confirmation_required", "执行写入需要有效 operation_id、confirm=true 和 8 至 128 字符的幂等键")
		}
		idemKey := fmt.Sprintf("%d:%s:%s", principal.UserID, name, key)
		s.mu.Lock()
		if taskID := s.idempotent[idemKey]; taskID != "" {
			s.mu.Unlock()
			return s.taskResult(principal, taskID, true)
		}
		op := s.operations[operationID]
		s.mu.Unlock()
		if op == nil || op.UserID != principal.UserID || op.Tool != name || time.Now().After(op.Expires) {
			return nil, toolError("mcp.operation_invalid", "操作标识不存在、已过期或不属于当前读者，请重新预检")
		}
		// Revalidate after confirmation so a stale preview cannot enlarge access.
		current, err := s.validateWrite(ctx, principal, name, op.Arguments)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(current, op.Summary) {
			return nil, toolError("mcp.operation_changed", "预检内容已变化，请重新预检")
		}
		taskID, err := randomID("mcp_")
		if err != nil {
			return nil, err
		}
		now := time.Now()
		item := &task{ID: taskID, UserID: principal.UserID, Kind: name, Status: background.StatusRunning, CreatedAt: now, UpdatedAt: now}
		s.mu.Lock()
		if existing := s.idempotent[idemKey]; existing != "" {
			s.mu.Unlock()
			return s.taskResult(principal, existing, true)
		}
		if s.operations[operationID] != op || !time.Now().Before(op.Expires) {
			s.mu.Unlock()
			return nil, toolError("mcp.operation_invalid", "操作已执行或过期，请重新预检")
		}
		s.tasks[taskID] = item
		s.idempotent[idemKey] = taskID
		delete(s.operations, operationID)
		s.mu.Unlock()
		go s.runOperation(*op, *item, principal)
		return s.taskResult(principal, taskID, false)
	default:
		return nil, toolError("mcp.invalid_params", "phase 只能是 prepare 或 execute")
	}
}

func (s *Service) validateWrite(ctx context.Context, principal Principal, name string, args map[string]any) (map[string]any, error) {
	reader, err := userdb.GetReaderByID(s.DB, principal.UserID)
	if err != nil || reader == nil || !reader.Active {
		return nil, toolError("mcp.permission_denied", "当前读者未激活或不存在")
	}
	switch name {
	case "book_send":
		if !hasPermission(reader.Permission, "p") {
			return nil, toolError("mcp.permission_denied", "当前读者没有推送权限")
		}
		bookID := int64(intArg(args["book_id"], 0))
		book, err := s.visibleBook(ctx, principal, bookID)
		if err != nil {
			return nil, err
		}
		deviceType := strings.ToLower(stringArg(args["device_type"]))
		target := stringArg(args["target"])
		var selected *userdb.Device
		if id := int64(intArg(args["device_id"], 0)); id > 0 {
			selected, err = s.device(principal.UserID, id)
			if err != nil {
				return nil, err
			}
			deviceType = selected.DeviceType
			target = deviceTarget(selected)
		}
		if deviceType == "" || len([]rune(deviceType)) > 40 || target == "" || len(target) > 512 {
			return nil, toolError("mcp.invalid_params", "设备类型和投递目标无效")
		}
		if deviceType == "kindle" && !strings.Contains(target, "@") {
			return nil, toolError("mcp.invalid_params", "Kindle 投递目标必须是邮箱")
		}
		if _, err = s.runtime().ProviderFor("integrations.push", map[string]any{"device_type": deviceType}); err != nil {
			return nil, toolError("push.unavailable", "对应推送插件不可用")
		}
		format := booktools.PickFormat(book, "EPUB", "MOBI", "AZW3", "PDF", "TXT")
		if requested := strings.ToUpper(stringArg(args["format"])); requested != "" {
			format = booktools.PickFormat(book, requested)
		}
		if format == "" {
			return nil, toolError("book.format_not_found", "书籍没有可推送的指定格式")
		}
		result := map[string]any{"action": "book_send", "book_id": book.ID, "title": book.Title, "device_type": deviceType, "target": target, "format": format}
		if selected != nil {
			result["device"] = selected
		}
		return result, nil
	case "device_manage":
		if !hasPermission(reader.Permission, "p") {
			return nil, toolError("mcp.permission_denied", "当前读者没有推送权限")
		}
		action := stringArg(args["action"])
		if action != "add" && action != "update" && action != "delete" {
			return nil, toolError("mcp.invalid_params", "请选择 add、update 或 delete")
		}
		result := map[string]any{"action": action}
		if action != "add" {
			d, e := s.device(principal.UserID, int64(intArg(args["device_id"], 0)))
			if e != nil {
				return nil, e
			}
			result["previous"] = d
		}
		if action != "delete" {
			d, e := deviceInput(args)
			if e != nil {
				return nil, e
			}
			if _, e = s.runtime().ProviderFor("integrations.push", map[string]any{"device_type": d.DeviceType}); e != nil {
				return nil, toolError("push.unavailable", "对应推送插件不可用")
			}
			result["new"] = d
		}
		return result, nil
	case "book_update":
		id := int64(intArg(args["book_id"], 0))
		book, err := s.visibleBook(ctx, principal, id)
		if err != nil {
			return nil, err
		}
		patch, err := metadataPatch(args)
		if err != nil {
			return nil, err
		}
		if s.EditMetadata == nil {
			return nil, toolError("book.edit_unavailable", "元数据编辑服务未装配")
		}
		if err = s.EditMetadata(ctx, principal, id, patch, false); err != nil {
			return nil, err
		}
		return map[string]any{"action": name, "book_id": id, "current_title": book.Title, "changes": patch}, nil
	case "book_import":
		if !hasPermission(reader.Permission, "s") {
			return nil, toolError("mcp.permission_denied", "当前读者没有保存书籍权限")
		}
		if s.Import == nil {
			return nil, toolError("import.unavailable", "在线导入服务未装配")
		}
		sourceKey, bookURL := stringArg(args["source_key"]), stringArg(args["book_url"])
		if sourceKey == "" || len(sourceKey) > 200 || bookURL == "" || len(bookURL) > 2048 {
			return nil, toolError("mcp.invalid_params", "书源标识或书籍外部标识无效")
		}
		catalog, err := sourcecatalog.NewService(s.DB, s.Settings, principal.UserID, s.runtime())
		if err != nil {
			return nil, err
		}
		binding, err := catalog.Get(sourceKey)
		if err != nil || binding == nil || binding.DownloadMode == "none" {
			return nil, toolError("source.unavailable", "书源不存在、不可见或不支持导入")
		}
		return map[string]any{"action": "book_import", "source_key": sourceKey, "source_name": binding.Name, "book_url": bookURL, "format": stringArg(args["format"]), "clean": boolArg(args["clean"])}, nil
	default:
		return nil, toolError("mcp.tool_not_found", "写入工具不存在")
	}
}

func (s *Service) runOperation(op operation, item task, principal Principal) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var err error
	switch op.Tool {
	case "book_send":
		if id := int64(intArg(op.Arguments["device_id"], 0)); id > 0 {
			var d *userdb.Device
			d, err = s.device(principal.UserID, id)
			if err == nil && !reflect.DeepEqual(d, op.Summary["device"]) {
				err = toolError("device.changed", "设备已变化，请重新预检")
			}
		}
		if err == nil {
			err = s.performSend(ctx, principal, op.Arguments, &item)
		}
	case "device_manage":
		item.Result, err = s.changeDevice(ctx, principal, op)
	case "book_import":
		err = s.performImport(ctx, principal, op.Arguments, &item)
	case "book_update":
		item.BookID = int64(intArg(op.Arguments["book_id"], 0))
		var patch map[string]any
		patch, err = metadataPatch(op.Arguments)
		if err == nil {
			if s.AccessResolver != nil {
				principal.Access, err = s.AccessResolver(ctx, principal.UserID, principal.IsAdmin)
			}
			if err == nil {
				err = s.EditMetadata(ctx, principal, item.BookID, patch, true)
			}
		}
		if err == nil {
			item.Result = map[string]any{"book_id": item.BookID, "updated": true}
		}
	default:
		err = fmt.Errorf("unknown operation")
	}
	s.mu.Lock()
	current := s.tasks[item.ID]
	if current != nil {
		current.UpdatedAt = time.Now()
		current.BookID = item.BookID
		current.BackgroundID = item.BackgroundID
		if err != nil {
			log.Printf("MCP task failed user=%d tool=%s task=%s: %v", principal.UserID, op.Tool, item.ID, err)
			current.Status = background.StatusFailed
			current.Error = "操作失败，请检查连接、权限和服务器日志"
		} else {
			current.Status = background.StatusCompleted
			current.Result = item.Result
		}
	}
	s.mu.Unlock()
}

func (s *Service) performSend(ctx context.Context, principal Principal, args map[string]any, item *task) error {
	book, err := s.visibleBook(ctx, principal, int64(intArg(args["book_id"], 0)))
	if err != nil {
		return err
	}
	format := booktools.PickFormat(book, "EPUB", "MOBI", "AZW3", "PDF", "TXT")
	if requested := stringArg(args["format"]); requested != "" {
		format = booktools.PickFormat(book, requested)
	}
	var path string
	var release func()
	if principal.Access != nil && !principal.Access.Unrestricted() && !principal.IsAdmin {
		file, resolveErr := (library.AccessStore{DB: s.LibraryDB}).AccessResolveFile(ctx, *principal.Access, book.ID, format, s.LibraryRoot)
		if resolveErr != nil {
			return resolveErr
		}
		path, release, err = sourcefile.Acquire(ctx, file.Path)
	} else {
		path, release, err = booktools.AcquireFormatPath(ctx, s.LibraryDB, book.ID, format)
	}
	if err != nil {
		return err
	}
	defer release()
	deviceType := strings.ToLower(stringArg(args["device_type"]))
	target := stringArg(args["target"])
	if id := int64(intArg(args["device_id"], 0)); id > 0 {
		d, e := s.device(principal.UserID, id)
		if e != nil {
			return e
		}
		deviceType = d.DeviceType
		target = deviceTarget(d)
	}
	provider, err := s.runtime().ProviderFor("integrations.push", map[string]any{"device_type": deviceType})
	if err != nil {
		return err
	}
	push, ok := provider.(pluginiface.PushProvider)
	if !ok {
		return fmt.Errorf("provider does not push")
	}
	_, err = push.Push(path, target, map[string]any{
		"platform":    map[string]any{"user_id": principal.UserID, "site_url": settingString(s.Settings, "site_url"), "book": map[string]any{"title": book.Title, "fmt_" + strings.ToLower(format): path}},
		"device_type": deviceType, "book_id": book.ID, "format": strings.ToLower(format), "title": book.Title,
	})
	if err != nil {
		return err
	}
	item.BookID = book.ID
	item.Result = map[string]any{"book_id": book.ID, "title": book.Title, "format": format, "device_type": deviceType, "delivered": true}
	return nil
}

func (s *Service) performImport(ctx context.Context, principal Principal, args map[string]any, item *task) error {
	backgroundTask := s.Background.AddTask(background.ServiceTypeOnlineSave, "MCP 在线导入", 0, "mcp:"+item.ID)
	var backgroundID int64
	if backgroundTask != nil {
		backgroundID = backgroundTask.ID
		item.BackgroundID = backgroundID
	}
	err := s.Import(ctx, principal, stringArg(args["source_key"]), stringArg(args["book_url"]), stringArg(args["format"]), boolArg(args["clean"]), backgroundID)
	if err != nil {
		if backgroundID != 0 {
			s.Background.CompleteTask(backgroundID, "MCP import failed")
		}
		return err
	}
	result := map[string]any{"imported": true, "source_key": stringArg(args["source_key"])}
	if backgroundID != 0 {
		s.Background.CompleteTask(backgroundID, "")
		if progress := s.Background.GetTask(backgroundID); progress != nil {
			if data, ok := progress["progress_data"].(map[string]any); ok {
				if id, valid := data["book_id"].(int64); valid {
					item.BookID = id
					result["book_id"] = id
				}
			}
		}
	}
	item.Result = result
	return nil
}

func (s *Service) taskStatus(principal Principal, args map[string]any) (any, error) {
	id := stringArg(args["task_id"])
	if id == "" {
		return nil, toolError("mcp.invalid_params", "task_id 必填")
	}
	s.mu.Lock()
	item := s.tasks[id]
	s.mu.Unlock()
	if item == nil || item.UserID != principal.UserID {
		return nil, toolError("task.not_found", "任务不存在或不属于当前读者")
	}
	return s.taskResult(principal, id, false)
}

func (s *Service) taskResult(principal Principal, id string, duplicate bool) (any, error) {
	s.mu.Lock()
	item := s.tasks[id]
	if item == nil || item.UserID != principal.UserID {
		s.mu.Unlock()
		return nil, toolError("task.not_found", "任务不存在或不属于当前读者")
	}
	copy := *item
	s.mu.Unlock()
	result := map[string]any{"task_id": copy.ID, "kind": copy.Kind, "status": copy.Status, "created_at": copy.CreatedAt.UTC().Format(time.RFC3339), "updated_at": copy.UpdatedAt.UTC().Format(time.RFC3339), "duplicate": duplicate}
	if copy.BackgroundID != 0 {
		if progress := s.Background.GetTask(copy.BackgroundID); progress != nil {
			result["progress"] = progress["progress"]
			if data, ok := progress["progress_data"].(map[string]any); ok {
				safe := map[string]any{}
				for _, key := range []string{"done", "total", "book_id"} {
					if value, exists := data[key]; exists {
						safe[key] = value
					}
				}
				result["progress_data"] = safe
			}
		}
	}
	if copy.BookID != 0 {
		result["book_id"] = copy.BookID
	}
	if copy.Result != nil {
		result["result"] = copy.Result
	}
	if copy.Error != "" {
		result["error"] = map[string]any{"code": "mcp.operation_failed", "message": copy.Error}
	}
	return result, nil
}

func (s *Service) expireLocked() {
	now := time.Now()
	for id, op := range s.operations {
		if now.After(op.Expires) {
			delete(s.operations, id)
		}
	}
	if len(s.operations) > 1000 {
		s.operations = map[string]*operation{}
	}
	if len(s.tasks) > 1000 {
		for id, item := range s.tasks {
			if item.Status != background.StatusRunning && now.Sub(item.UpdatedAt) > 24*time.Hour {
				delete(s.tasks, id)
			}
		}
	}
}
func randomID(prefix string) (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value), nil
}
func settingString(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}

// metadataPatch accepts only editable metadata and leaves omitted fields alone.
func metadataPatch(args map[string]any) (map[string]any, error) {
	patch := make(map[string]any)
	for _, key := range []string{"title", "publisher", "series", "isbn"} {
		if v, exists := args[key]; exists {
			text, ok := v.(string)
			if !ok || len([]rune(text)) > 512 || text == "__DELETE__" {
				return nil, toolError("mcp.invalid_params", "元数据字段必须是有效字符串")
			}
			patch[key] = strings.TrimSpace(text)
		}
	}
	if stringArg(patch["title"]) == "" {
		return nil, toolError("mcp.invalid_params", "书名必填")
	}
	for _, key := range []string{"authors", "tags"} {
		if v, exists := args[key]; exists {
			items, ok := v.([]any)
			if !ok || len(items) > 50 {
				return nil, toolError("mcp.invalid_params", "作者和标签必须是字符串数组，最多 50 项")
			}
			values := make([]string, 0, len(items))
			for _, item := range items {
				name, ok := item.(string)
				name = strings.TrimSpace(name)
				if !ok || name == "" || len([]rune(name)) > 200 || name == "__DELETE__" {
					return nil, toolError("mcp.invalid_params", "作者或标签无效")
				}
				values = append(values, name)
			}
			patch[key] = values
		}
	}
	return patch, nil
}
