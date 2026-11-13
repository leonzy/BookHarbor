package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"talebook/booksvr/internal/access"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

// AccessAdminConfig exposes drafts while gating publication on all readers.
type AccessAdminConfig struct {
	Store library.AccessStore
	// The runtime supplies this only after every resource-serving process has
	// advertised support. Absence fails closed, including for administrators.
	CheckRuntime func(context.Context) error
}

type accessActionRequest struct {
	Version   int64 `json:"version"`
	Watermark int64 `json:"watermark"`
}

func accessRevisionAction(cfg AccessAdminConfig, action string) HandlerFunc {
	return func(ctx *Context) (map[string]any, error) {
		id, err := strconv.ParseInt(ctx.Param(0), 10, 64)
		if err != nil || id < 1 {
			return map[string]any{"err": "params.invalid"}, nil
		}
		var body accessActionRequest
		reader := http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096)
		defer reader.Close()
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&body); err != nil || body.Version < 1 || body.Watermark < 0 {
			return map[string]any{"err": "params.invalid"}, nil
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return map[string]any{"err": "params.invalid"}, nil
		}
		c := ctx.Request.Context()
		switch action {
		case "prepare":
			err = cfg.Store.BeginPreparation(c, id, body.Version)
		case "cancel":
			err = cfg.Store.CancelPreparation(c, id, body.Version)
		case "apply":
			if cfg.CheckRuntime == nil {
				return map[string]any{"err": "access.runtime_not_ready", "msg": "阅读服务尚未全部支持权限控制，当前规则未改变"}, nil
			}
			if err = cfg.CheckRuntime(c); err != nil {
				return map[string]any{"err": "access.runtime_not_ready", "msg": "阅读服务权限检查未通过，当前规则未改变"}, nil
			}
			var actor string
			actor, err = cfg.Store.RegisterPrincipal(c, ctx.Identity.UserID)
			if err == nil {
				err = cfg.Store.PublishAccess(c, id, body.Version, body.Watermark, actor)
			}
			if err == nil && ctx.reauthorize != nil {
				// This response describes the newly published revision. Refresh
				// its guard; unrelated in-flight responses must still be revoked.
				err = ctx.reauthorize(ctx.Identity)
			}
		}
		if err != nil {
			return accessAdminError(err)
		}
		r, err := cfg.Store.Revision(c, id)
		if err != nil {
			return accessAdminError(err)
		}
		return map[string]any{"err": "ok", "revision": r}, nil
	}
}

type accessDraftRequest struct {
	Version      int64         `json:"version"`
	BaseRevision int64         `json:"base_revision"`
	Policy       access.Policy `json:"policy"`
}

func accessAdminError(err error) (map[string]any, error) {
	if errors.Is(err, library.ErrAccessConflict) {
		return map[string]any{"err": "access.conflict", "msg": "规则已被其他操作修改，请重新加载并检查差异"}, nil
	}
	if errors.Is(err, library.ErrAccessNotReady) {
		return map[string]any{"err": "access.not_ready", "msg": "权限范围尚未准备完成"}, nil
	}
	return nil, err
}

func readAccessDraft(ctx *Context) (accessDraftRequest, error) {
	var body accessDraftRequest
	reader := http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1<<20)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return body, errors.New("multiple or invalid JSON values")
	}
	if body.Version < 0 || body.BaseRevision < 0 {
		return body, errors.New("invalid revision")
	}
	return body, nil
}

func AccessAdminRouteSpecs(cfg AccessAdminConfig) []RouteSpec {
	save := func(ctx *Context) (map[string]any, error) {
		body, err := readAccessDraft(ctx)
		if err != nil {
			return map[string]any{"err": "params.invalid", "msg": "权限草稿格式无效或超过大小限制"}, nil
		}
		// Origin inheritance is maintained by the source identity subsystem.
		// The ordinary rule editor cannot silently erase historical protection.
		if len(body.Policy.OriginRules) != 0 {
			return map[string]any{"err": "params.invalid", "msg": "来源继承规则不能通过普通草稿编辑修改"}, nil
		}
		id := int64(0)
		if raw := ctx.Param(0); raw != "" {
			id, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 1 {
				return map[string]any{"err": "params.invalid"}, nil
			}
		}
		originRevision := body.BaseRevision
		if id > 0 {
			originRevision = id
		}
		if originRevision > 0 {
			r, e := cfg.Store.Revision(ctx.Request.Context(), originRevision)
			if e != nil {
				return accessAdminError(e)
			}
			body.Policy.OriginRules = r.Policy.OriginRules
		}
		actor, err := cfg.Store.RegisterPrincipal(ctx.Request.Context(), ctx.Identity.UserID)
		if err != nil {
			return accessAdminError(err)
		}
		r, err := cfg.Store.SaveDraft(ctx.Request.Context(), id, body.Version, body.BaseRevision, actor, body.Policy)
		if err != nil {
			return accessAdminError(err)
		}
		return map[string]any{"err": "ok", "revision": r}, nil
	}
	return []RouteSpec{
		{Pattern: "/api/admin/access", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
			o, err := cfg.Store.AccessOverview(ctx.Request.Context())
			if err != nil {
				return accessAdminError(err)
			}
			if ctx.DB != nil {
				readers, e := userdb.ListReaders(ctx.DB)
				if e != nil {
					return accessAdminError(e)
				}
				byID := make(map[int64]*userdb.Reader, len(readers))
				for _, reader := range readers {
					byID[reader.ID] = reader
				}
				for i := range o.Principals {
					if reader := byID[o.Principals[i].AccountID]; reader != nil {
						o.Principals[i].Username, o.Principals[i].Name = reader.Username, reader.Name
						o.Principals[i].Admin = reader.Admin
					}
				}
			}
			return map[string]any{"err": "ok", "access": o}, nil
		}},
		{Pattern: "/api/admin/access/directory", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
			start := 0
			var err error
			if ctx.Query("start") != "" {
				start, err = strconv.Atoi(ctx.Query("start"))
				if err != nil || start < 0 {
					return map[string]any{"err": "params.invalid"}, nil
				}
			}
			dir := ctx.Query("directory")
			if dir == "" {
				dir = "/"
			}
			items, more, err := cfg.Store.AccessDirectoryChildren(ctx.Request.Context(), ctx.Query("root"), dir, start)
			if err != nil {
				return accessAdminError(err)
			}
			return map[string]any{"err": "ok", "directories": items, "has_more": more}, nil
		}},
		{Pattern: "/api/admin/access/revision/([0-9]+)/prepare", Methods: []string{"POST"}, Auth: true, Admin: true, Handler: accessRevisionAction(cfg, "prepare")},
		{Pattern: "/api/admin/access/revision/([0-9]+)/cancel", Methods: []string{"POST"}, Auth: true, Admin: true, Handler: accessRevisionAction(cfg, "cancel")},
		{Pattern: "/api/admin/access/revision/([0-9]+)/apply", Methods: []string{"POST"}, Auth: true, Admin: true, Handler: accessRevisionAction(cfg, "apply")},
		{Pattern: "/api/admin/access/revision", Methods: []string{"POST"}, Auth: true, Admin: true, Handler: save},
		{Pattern: "/api/admin/access/revision/([0-9]+)", Methods: []string{"GET", "PUT"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
			if ctx.Request.Method == http.MethodPut {
				return save(ctx)
			}
			id, err := strconv.ParseInt(ctx.Param(0), 10, 64)
			if err != nil || id < 1 {
				return map[string]any{"err": "params.invalid"}, nil
			}
			r, err := cfg.Store.Revision(ctx.Request.Context(), id)
			if err != nil {
				return accessAdminError(err)
			}
			return map[string]any{"err": "ok", "revision": r}, nil
		}},
		{Pattern: "/api/admin/access/revision/([0-9]+)/preview", Methods: []string{"GET"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
			if ctx.DB != nil {
				var account int64
				if err := cfg.Store.DB.QueryRowContext(ctx.Request.Context(), `SELECT COALESCE(account_id,0) FROM acl_principal WHERE id=? AND disabled=0`, ctx.Query("principal")).Scan(&account); err == nil && account > 0 {
					reader, err := userdb.GetReaderByID(ctx.DB, account)
					if err != nil {
						return nil, err
					}
					if reader != nil && reader.Admin {
						return map[string]any{"err": "params.invalid", "msg": "管理员始终可见全部书目，请选择普通用户或游客预览"}, nil
					}
				}
			}
			id, err := strconv.ParseInt(ctx.Param(0), 10, 64)
			if err != nil || id < 1 {
				return map[string]any{"err": "params.invalid"}, nil
			}
			start := 0
			if raw := ctx.Query("start"); raw != "" {
				start, err = strconv.Atoi(raw)
				if err != nil || start < 0 {
					return map[string]any{"err": "params.invalid"}, nil
				}
			}
			scope, err := cfg.Store.PreviewAccess(ctx.Request.Context(), id, ctx.Query("principal"))
			if err != nil {
				return accessAdminError(err)
			}
			order := ctx.Query("sort")
			if order == "" {
				order = "title"
			}
			if order != "title" && order != "size" && order != "newest" {
				return map[string]any{"err": "params.invalid"}, nil
			}
			books, total, err := cfg.Store.AccessPreviewPage(ctx.Request.Context(), scope, start, 50, order)
			if err != nil {
				return accessAdminError(err)
			}
			return map[string]any{"err": "ok", "books": books, "total": total, "count_kind": "edition_records", "start": start, "page_size": 50, "read_only": true}, nil
		}},
	}
}
