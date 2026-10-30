package httpapi

import (
	"database/sql"
	"talebook/booksvr/internal/userdb"
)

func newOpdsConfig(db, userDB *sql.DB, opts ServeOptions) OpdsConfig {
	blocked := func(ctx *Context) (map[int64]bool, error) {
		return userdb.PrivateBookIDsFor(userDB, ctx.Identity.UserID, ctx.Identity.IsAdmin)
	}
	return OpdsConfig{DB: db, Settings: opts.Settings, SettingsPath: opts.SettingsPath, ExcludedBookIDs: blocked, FilterVisibleBookIDs: func(ctx *Context, ids []int64) ([]int64, error) {
		private, err := blocked(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]int64, 0, len(ids))
		for _, id := range ids {
			if !private[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}}
}
