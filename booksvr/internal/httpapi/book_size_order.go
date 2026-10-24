package httpapi

import "talebook/booksvr/internal/library"

func bookSizeSort(ctx *Context) string { return library.SizeOrder(ctx.Query("sort")) }
func adminSizeOrder(key string, desc bool) string {
	if key != "size" {
		return ""
	}
	if desc {
		return "desc"
	}
	return "asc"
}
