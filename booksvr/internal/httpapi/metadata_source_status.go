package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"strconv"

	"talebook/booksvr/internal/pluginruntime"
)

const rMetadataSource = "/api/admin/metadata/enrichment/sources/([0-9]+)"

func ensureMetadataDiagnosticsIndex(db *sql.DB) error {
	if db == nil {
		return nil
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='plugin_runs')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS metadata_failed_runs ON plugin_runs(connection_id,id DESC) WHERE status='failed'`)
	return err
}

func metadataSourceRoutes(db, userDB *sql.DB, opts ServeOptions) []RouteSpec {
	return []RouteSpec{{Pattern: rMetadataSource, Methods: []string{"PUT"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
		id, e := strconv.ParseInt(ctx.Param(0), 10, 64)
		if e != nil {
			return map[string]any{"err": "params.invalid", "msg": "来源不存在"}, nil
		}
		w := newBookWiring(userDB, db, opts.Settings, opts.PluginRegistry)
		rt := pluginruntime.NewRuntime(userDB, opts.Settings, w.registry)
		conns, e := rt.ConnectionsFor(metadataLookupCapability, &ctx.Identity.UserID)
		if e != nil {
			return nil, e
		}
		allowed := false
		for _, c := range conns {
			if c.ID == id {
				allowed = true
				break
			}
		}
		if !allowed {
			return map[string]any{"err": "permission", "msg": "无权配置此来源"}, nil
		}
		var body struct {
			Interval json.Number `json:"interval_seconds"`
		}
		dec := json.NewDecoder(io.LimitReader(ctx.Request.Body, 2048))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&body); e != nil {
			return map[string]any{"err": "params.invalid", "msg": "请输入1至3600之间的整数秒数"}, nil
		}
		seconds, e := body.Interval.Int64()
		if e != nil || seconds < 1 || seconds > 3600 {
			return map[string]any{"err": "params.invalid", "msg": "请输入1至3600之间的整数秒数"}, nil
		}
		if _, e = db.Exec(`INSERT INTO metadata_source_limits(connection_id,interval_seconds) VALUES(?,?) ON CONFLICT(connection_id) DO UPDATE SET interval_seconds=excluded.interval_seconds,next_at=CASE WHEN metadata_source_limits.failures>0 THEN max(metadata_source_limits.next_at,metadata_source_limits.last_request_at+excluded.interval_seconds) ELSE metadata_source_limits.last_request_at+excluded.interval_seconds END`, id, seconds); e != nil {
			return nil, e
		}
		return map[string]any{"err": "ok", "interval_seconds": seconds}, nil
	}}}
}

func metadataSourceStatus(db, userDB *sql.DB, opts ServeOptions, ctx *Context) ([]map[string]any, error) {
	out := []map[string]any{}
	if userDB == nil {
		return out, nil
	}
	w := newBookWiring(userDB, db, opts.Settings, opts.PluginRegistry)
	worker := &metadataWorker{w: w}
	rt := pluginruntime.NewRuntime(userDB, opts.Settings, w.registry)
	conns, e := rt.ConnectionsFor(metadataLookupCapability, &ctx.Identity.UserID)
	if e != nil {
		return nil, e
	}
	for _, c := range conns {
		interval := int64(5)
		var next, requests, failures int64
		var reason string
		e = db.QueryRow(`SELECT interval_seconds,next_at,requests,failures,last_error FROM metadata_source_limits WHERE connection_id=?`, c.ID).Scan(&interval, &next, &requests, &failures, &reason)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		unavailable := worker.sourceUnavailable(rt, c)
		var done, matched int64
		if e = db.QueryRow(`SELECT count(*),coalesce(sum(matched),0) FROM metadata_source_jobs WHERE connection_id=? AND phase='done'`, c.ID).Scan(&done, &matched); e != nil {
			return nil, e
		}
		key, e := rt.PluginKeyOf(c)
		if e != nil {
			return nil, e
		}
		name := c.Name
		definition, e := rt.DefinitionOf(c)
		if e != nil {
			return nil, e
		}
		if definition != nil && definition.Name != "" {
			name = definition.Name
			if c.Role != pluginruntime.BuiltinConnectionRole && c.Name != "" && c.Name != definition.Name {
				name += " · " + c.Name
			}
		}
		var runID int64
		errCode := ""
		e = userDB.QueryRow(`SELECT id,coalesce(error_code,'') FROM plugin_runs WHERE connection_id=? AND status='failed' AND json_extract(input_data,'$.background_metadata')=1 ORDER BY id DESC LIMIT 1`, c.ID).Scan(&runID, &errCode)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		out = append(out, map[string]any{"id": c.ID, "name": name, "plugin_key": key, "interval_seconds": interval, "next_at": next, "requests": requests, "processed": done, "matched": matched, "failures": failures, "last_error": reason, "unavailable": unavailable, "last_failure_run": runID, "last_failure_code": errCode})
	}
	return out, nil
}
