//go:build wasip1

package gopress

import "github.com/extism/go-pdk"

// The wasm exports; each runs the registered handler (handlers.go).

func run(export string) int32 {
	out, err := exports[export](pdk.Input())
	if err != nil {
		pdk.SetError(err)
		return 1
	}
	pdk.Output(out)
	return 0
}

//go:wasmexport render_shortcode
func renderShortcode() int32 { return run("render_shortcode") }

//go:wasmexport render_widget
func renderWidget() int32 { return run("render_widget") }

//go:wasmexport filter
func filter() int32 { return run("filter") }

//go:wasmexport comment_pre_save
func commentPreSave() int32 { return run("comment_pre_save") }

//go:wasmexport action
func action() int32 { return run("action") }

//go:wasmexport http_route
func httpRoute() int32 { return run("http_route") }

//go:wasmexport admin_page
func adminPage() int32 { return run("admin_page") }

//go:wasmexport admin_submit
func adminSubmit() int32 { return run("admin_submit") }

//go:wasmexport admin_api
func adminAPI() int32 { return run("admin_api") }

//go:wasmexport dashboard_widget
func dashboardWidget() int32 { return run("dashboard_widget") }

//go:wasmexport migrate_wordpress
func migrateWordPress() int32 { return run("migrate_wordpress") }
