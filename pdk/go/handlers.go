package gopress

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/extism/go-pdk"
)

// Handlers are registered in init() (GoPress loads plugins as WASI
// reactors: package initialization runs, main does not).

var (
	shortcodes  = map[string]func(ShortcodeRequest) (Render, error){}
	widgets     = map[string]func(WidgetRequest) (Render, error){}
	filters     = map[string]func(FilterRequest) (FilterResult, error){}
	actions     = map[string]func(json.RawMessage) error{}
	routes      []route
	tables      = map[string]func(AdminRequest) (Table, error){}
	submits     = map[string]func(AdminRequest) (string, error){}
	apis        = map[string]func(AdminRequest) (any, error){}
	dashboards  = map[string]func(DashboardRequest) (Render, error){}
	commentHook func(CommentCheck) (string, error)
	migrate     func() (string, error)
)

type route struct {
	path   string // "/ping" or "/api/*"
	handle func(Request) Response
}

// HandleShortcode registers the renderer of a shortcode.
func HandleShortcode(name string, fn func(ShortcodeRequest) (Render, error)) { shortcodes[name] = fn }

// HandleWidget registers the renderer of a widget type.
func HandleWidget(typ string, fn func(WidgetRequest) (Render, error)) { widgets[typ] = fn }

// HandleFilter registers a filter for a hook (the_content, head, footer).
func HandleFilter(hook string, fn func(FilterRequest) (FilterResult, error)) { filters[hook] = fn }

// HandleAction registers a handler for an action (activate, deactivate,
// comment_status_changed).
func HandleAction(action string, fn func(data json.RawMessage) error) { actions[action] = fn }

// HandleComment registers the comment_pre_save hook; it returns a verdict
// (Keep, Approve, Hold, Spam or Discard).
func HandleComment(fn func(CommentCheck) (string, error)) { commentHook = fn }

// HandleRoute registers a handler for a route of the manifest ("/ipn", or
// "/api/*" for everything below /api/).
func HandleRoute(path string, fn func(Request) Response) { routes = append(routes, route{path, fn}) }

// HandleTable registers the data source of a table page.
func HandleTable(page string, fn func(AdminRequest) (Table, error)) { tables[page] = fn }

// HandleSubmit registers the bulk/row actions of a table page; the result
// is a notice shown to the user.
func HandleSubmit(page string, fn func(AdminRequest) (string, error)) { submits[page] = fn }

// HandleAPI registers the bridge calls of an iframe page
// (gopress.call(method, params) in the page's script).
func HandleAPI(page string, fn func(AdminRequest) (any, error)) { apis[page] = fn }

// HandleDashboard registers a dashboard box.
func HandleDashboard(id string, fn func(DashboardRequest) (Render, error)) { dashboards[id] = fn }

// HandleMigrate registers the WordPress import, run once on activation.
func HandleMigrate(fn func() (string, error)) { migrate = fn }

var errNotImplemented = errors.New("not implemented")

// serve decodes the input, runs fn and writes its output.
func serve[In, Out any](fn func(In) (Out, error)) int32 {
	var in In
	if err := json.Unmarshal(pdk.Input(), &in); err != nil {
		pdk.SetError(err)
		return 1
	}
	out, err := fn(in)
	if err != nil {
		pdk.SetError(err)
		return 1
	}
	b, err := json.Marshal(out)
	if err != nil {
		pdk.SetError(err)
		return 1
	}
	pdk.Output(b)
	return 0
}

//go:wasmexport render_shortcode
func renderShortcode() int32 {
	return serve(func(in ShortcodeRequest) (Render, error) {
		if fn := shortcodes[in.Tag]; fn != nil {
			return fn(in)
		}
		return Render{}, errNotImplemented
	})
}

//go:wasmexport render_widget
func renderWidget() int32 {
	return serve(func(in WidgetRequest) (Render, error) {
		if fn := widgets[in.Type]; fn != nil {
			return fn(in)
		}
		return Render{}, errNotImplemented
	})
}

//go:wasmexport filter
func filter() int32 {
	return serve(func(in FilterRequest) (FilterResult, error) {
		if fn := filters[in.Hook]; fn != nil {
			return fn(in)
		}
		return FilterResult{Value: in.Value}, nil
	})
}

//go:wasmexport comment_pre_save
func commentPreSave() int32 {
	return serve(func(in CommentCheck) (commentVerdict, error) {
		if commentHook == nil {
			return commentVerdict{}, nil
		}
		v, err := commentHook(in)
		return commentVerdict{v}, err
	})
}

//go:wasmexport action
func action() int32 {
	return serve(func(in actionIn) (struct{}, error) {
		if fn := actions[in.Action]; fn != nil {
			return struct{}{}, fn(in.Data)
		}
		return struct{}{}, nil
	})
}

//go:wasmexport http_route
func httpRoute() int32 {
	return serve(func(in Request) (Response, error) {
		for _, r := range routes {
			if prefix, ok := strings.CutSuffix(r.path, "/*"); ok {
				if strings.HasPrefix(in.Path, prefix+"/") {
					return r.handle(in), nil
				}
			} else if in.Path == r.path {
				return r.handle(in), nil
			}
		}
		return Response{Status: 404, Body: "not found"}, nil
	})
}

//go:wasmexport admin_page
func adminPage() int32 {
	return serve(func(in AdminRequest) (Table, error) {
		if fn := tables[in.Page]; fn != nil {
			return fn(in)
		}
		return Table{}, errNotImplemented
	})
}

//go:wasmexport admin_submit
func adminSubmit() int32 {
	return serve(func(in AdminRequest) (notice, error) {
		if fn := submits[in.Page]; fn != nil {
			n, err := fn(in)
			return notice{n}, err
		}
		return notice{}, errNotImplemented
	})
}

//go:wasmexport admin_api
func adminAPI() int32 {
	return serve(func(in AdminRequest) (any, error) {
		if fn := apis[in.Page]; fn != nil {
			return fn(in)
		}
		return nil, errNotImplemented
	})
}

//go:wasmexport dashboard_widget
func dashboardWidget() int32 {
	return serve(func(in DashboardRequest) (Render, error) {
		if fn := dashboards[in.ID]; fn != nil {
			return fn(in)
		}
		return Render{}, errNotImplemented
	})
}

//go:wasmexport migrate_wordpress
func migrateWordPress() int32 {
	return serve(func(struct{}) (notice, error) {
		if migrate == nil {
			return notice{}, nil
		}
		n, err := migrate()
		return notice{n}, err
	})
}
