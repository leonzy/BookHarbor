package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

func AccessClassRouteSpecs(cfg AccessAdminConfig) []RouteSpec {
	return []RouteSpec{{Pattern: "/api/admin/access/revision/([0-9]+)/class/([a-zA-Z0-9_-]+)/books", Methods: []string{"GET", "PUT"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
		id, err := strconv.ParseInt(ctx.Param(0), 10, 64)
		if err != nil || id < 1 {
			return map[string]any{"err": "params.invalid"}, nil
		}
		if ctx.Request.Method == "GET" {
			start := 0
			if raw := ctx.Query("start"); raw != "" {
				start, err = strconv.Atoi(raw)
				if err != nil || start < 0 {
					return map[string]any{"err": "params.invalid"}, nil
				}
			}
			books, total, e := cfg.Store.AccessClassBooks(ctx.Request.Context(), id, ctx.Param(1), start)
			if e != nil {
				return accessAdminError(e)
			}
			return map[string]any{"err": "ok", "books": books, "total": total}, nil
		}
		var body struct {
			Version  int64   `json:"version"`
			Books    []int64 `json:"books"`
			Assigned *bool   `json:"assigned"`
		}
		reader := http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 16384)
		defer reader.Close()
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.Version < 1 || body.Assigned == nil {
			return map[string]any{"err": "params.invalid"}, nil
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return map[string]any{"err": "params.invalid"}, nil
		}
		actor, e := cfg.Store.RegisterPrincipal(ctx.Request.Context(), ctx.Identity.UserID)
		if e != nil {
			return accessAdminError(e)
		}
		r, e := cfg.Store.AssignAccessClass(ctx.Request.Context(), id, body.Version, ctx.Param(1), body.Books, *body.Assigned, actor)
		if e != nil {
			return accessAdminError(e)
		}
		return map[string]any{"err": "ok", "revision": r}, nil
	}}}
}
