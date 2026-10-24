package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/site"
)

// Runtime limits.
const (
	maxInstances   = 4       // per plugin
	maxPages       = 1024    // 64 MB linear memory per instance
	maxOutputBytes = 2 << 20 // of one call
	maxInputBytes  = 4 << 20

	breakerFailures = 5
	breakerWindow   = time.Minute
	breakerPause    = 5 * time.Minute
)

// callKind selects the time limit of a call.
type callKind int

const (
	callRender  callKind = iota // shortcodes, widgets, content filter: 1 s
	callHook                    // comment hooks, actions, dashboard: 5 s
	callRequest                 // routes and admin calls, may use HTTP: 15 s
	callMigrate                 // WordPress import: 2 min
)

func (k callKind) timeout() time.Duration {
	switch k {
	case callRender:
		return time.Second
	case callHook:
		return 5 * time.Second
	case callRequest:
		return 15 * time.Second
	}
	return 2 * time.Minute
}

// Site is what the host needs from the public site.
type Site interface {
	Settings(ctx context.Context) (*site.Settings, error)
	Links(ctx context.Context) (*site.Links, error)
	Invalidate()
}

// Options configures the host.
type Options struct {
	DB    *db.DB
	Store *Store
	Site  Site
	Log   *slog.Logger
	// CacheDir keeps compiled modules between restarts ("" = in memory).
	CacheDir string
	// HTTP sends the plugins' HTTP requests; nil uses a client that only
	// connects to public addresses.
	HTTP *http.Client
}

// Host runs the active plugins.
type Host struct {
	opts  Options
	log   *slog.Logger
	now   func() time.Time
	cache wazero.CompilationCache
	http  *http.Client

	mu     sync.RWMutex
	active map[string]*plugin // by name
}

// plugin is an active plugin.
type plugin struct {
	h        *Host
	pkg      *Package
	perms    Permissions // the package's, covered by the approval
	approved string      // the approved set (canonical JSON)
	ready    chan struct{}
	compiled *extism.CompiledPlugin
	err      error // compile error

	sem  chan struct{}
	idle chan *extism.Plugin
	http chan struct{} // concurrent HTTP requests

	mu        sync.Mutex
	failures  []time.Time
	paused    time.Time
	lastError string
	logs      int // lines logged in the current minute
	logMinute int64
}

// New creates the host and starts the active plugins.
func New(ctx context.Context, opts Options) (*Host, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	h := &Host{opts: opts, log: opts.Log, now: time.Now, active: map[string]*plugin{}, http: opts.HTTP}
	if h.http == nil {
		h.http = safeClient()
	}
	if opts.CacheDir != "" {
		c, err := wazero.NewCompilationCacheWithDir(opts.CacheDir)
		if err != nil {
			return nil, err
		}
		h.cache = c
	} else {
		h.cache = wazero.NewCompilationCache()
	}
	if err := h.reload(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// Close stops all plugins.
func (h *Host) Close(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.active {
		p.close(ctx)
	}
	h.active = map[string]*plugin{}
	h.cache.Close(ctx)
}

// reload starts the plugins that are active and approved, and stops the
// others.
func (h *Host) reload(ctx context.Context) error {
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, p := range h.active {
		st := states[name]
		if st == nil || !st.Active || h.opts.Store.Get(name) != p.pkg || st.Approved != p.approved {
			p.close(ctx)
			delete(h.active, name)
		}
	}
	for name, st := range states {
		if !st.Active || h.active[name] != nil {
			continue
		}
		pkg := h.opts.Store.Get(name)
		if pkg == nil {
			continue
		}
		approved, err := ParsePermissions(st.Approved)
		if err != nil {
			continue
		}
		if ok, _ := pkg.Manifest.Permissions.Covers(approved); !ok {
			continue // an update asks for more: wait for approval
		}
		p := &plugin{h: h, pkg: pkg, perms: pkg.Manifest.Permissions, approved: st.Approved, ready: make(chan struct{}),
			sem: make(chan struct{}, maxInstances), idle: make(chan *extism.Plugin, maxInstances), http: make(chan struct{}, 4)}
		h.active[name] = p
		go p.compile(context.WithoutCancel(ctx))
	}
	return nil
}

func (p *plugin) compile(ctx context.Context) {
	defer close(p.ready)
	defer func() {
		if r := recover(); r != nil {
			p.err = fmt.Errorf("compile: %v", r)
		}
	}()
	m := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: p.pkg.Wasm, Name: "main"}},
		Memory:       &extism.ManifestMemory{MaxPages: maxPages, MaxVarBytes: 64 << 10, MaxHttpResponseBytes: 0},
		AllowedHosts: []string{}, // Extism's own HTTP is off; see the gopress host function
	}
	cfg := extism.PluginConfig{EnableWasi: true,
		RuntimeConfig: wazero.NewRuntimeConfig().WithCompilationCache(p.h.cache).WithCloseOnContextDone(true).
			WithMemoryLimitPages(maxPages)}
	p.compiled, p.err = extism.NewCompiledPlugin(ctx, m, cfg, []extism.HostFunction{p.hostFunction()})
	if p.err != nil {
		p.h.log.Error("plugin compile", "plugin", p.pkg.Manifest.Name, "err", p.err)
	}
}

func (p *plugin) close(ctx context.Context) {
	go func() { // wait for a running compile without blocking the caller
		<-p.ready
		for {
			select {
			case inst := <-p.idle:
				inst.Close(ctx)
			default:
				if p.compiled != nil {
					p.compiled.Close(ctx)
				}
				return
			}
		}
	}()
}

// ErrUnavailable is returned for calls to plugins that are not active,
// paused after errors or failed to load.
var ErrUnavailable = errors.New("plugin unavailable")

// get returns the active plugin name.
func (h *Host) get(name string) *plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active[name]
}

// activePlugins returns the active plugins (sorted by name).
func (h *Host) activePlugins() []*plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*plugin, 0, len(h.active))
	for _, p := range h.active {
		out = append(out, p)
	}
	sortPlugins(out)
	return out
}

func sortPlugins(list []*plugin) {
	slices.SortFunc(list, func(a, b *plugin) int { return strings.Compare(a.pkg.Manifest.Name, b.pkg.Manifest.Name) })
}

// call runs an export of a plugin with the limits of kind. in and out are
// JSON encoded; out may be nil.
func (h *Host) call(ctx context.Context, name, export string, kind callKind, in, out any) error {
	p := h.get(name)
	if p == nil {
		return ErrUnavailable
	}
	return p.call(ctx, export, kind, in, out)
}

func (p *plugin) call(ctx context.Context, export string, kind callKind, in, out any) (err error) {
	if p.isPaused() {
		return ErrUnavailable
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.err != nil {
		return ErrUnavailable
	}
	input, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(input) > maxInputBytes {
		return errors.New("plugin input too large")
	}
	ctx, cancel := context.WithTimeout(ctx, kind.timeout())
	defer cancel()
	select { // at most maxInstances calls at a time
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		p.fail(errors.New("busy"))
		return ErrUnavailable
	}
	var inst *extism.Plugin
	select {
	case inst = <-p.idle:
	default:
		if inst, err = p.compiled.Instance(ctx, extism.PluginInstanceConfig{ModuleConfig: wazero.NewModuleConfig().
			WithStdout(p.logWriter(slog.LevelInfo)).WithStderr(p.logWriter(slog.LevelWarn))}); err != nil {
			p.fail(err)
			return ErrUnavailable
		}
	}
	keep := false
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin call panicked: %v", r)
			keep = false
		}
		if keep {
			p.idle <- inst
		} else {
			inst.Close(context.Background())
		}
		var pe *PluginError
		if err != nil && !errors.Is(err, ErrUnavailable) && !errors.As(err, &pe) {
			p.fail(err) // crashes, time and size limits; not errors the plugin reports
		}
	}()
	rc, output, err := inst.CallWithContext(ctx, export, input)
	// An error the plugin set itself (pdk.SetError) is in the kernel's
	// error slot; traps and limits are not.
	if err != nil && rc != 0 && ctx.Err() == nil {
		if msg := inst.GetError(); msg != "" && msg == err.Error() {
			keep = true
			return &PluginError{Msg: truncate(msg, 500)}
		}
	}
	switch {
	case err != nil:
		if ctx.Err() != nil {
			err = fmt.Errorf("%s: time limit of %s exceeded", export, kind.timeout())
		} else {
			err = fmt.Errorf("%s: %w", export, err)
		}
		return err
	case rc != 0:
		keep = true // the plugin reported an error; its state is intact
		msg := inst.GetError()
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", rc)
		}
		return &PluginError{Msg: truncate(msg, 500)}
	case len(output) > maxOutputBytes:
		keep = true
		return errors.New(export + ": output too large")
	}
	keep = true
	if out != nil {
		if err := json.Unmarshal(output, out); err != nil {
			return fmt.Errorf("%s: invalid output: %w", export, err)
		}
	}
	return nil
}

// PluginError is an error the plugin reported itself.
type PluginError struct{ Msg string }

func (e *PluginError) Error() string { return e.Msg }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// fail counts a failure; too many in a short time pause the plugin.
func (p *plugin) fail(err error) {
	now := p.h.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastError = truncate(err.Error(), 300)
	keep := p.failures[:0]
	for _, t := range p.failures {
		if now.Sub(t) < breakerWindow {
			keep = append(keep, t)
		}
	}
	p.failures = append(keep, now)
	if len(p.failures) >= breakerFailures && now.After(p.paused) {
		p.paused = now.Add(breakerPause)
		p.failures = nil
		p.h.log.Error("plugin paused after repeated errors", "plugin", p.pkg.Manifest.Name, "err", err, "until", p.paused)
		return
	}
	p.h.log.Warn("plugin error", "plugin", p.pkg.Manifest.Name, "err", err)
}

func (p *plugin) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h.now().Before(p.paused)
}

// Status is the runtime state of a plugin for the admin.
type Status struct {
	Running   bool
	Paused    bool
	LastError string
}

// Status returns the runtime state of plugin name.
func (h *Host) Status(name string) Status {
	p := h.get(name)
	if p == nil {
		return Status{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Status{Running: true, Paused: h.now().Before(p.paused), LastError: p.lastError}
	select {
	case <-p.ready:
		if p.err != nil {
			st.Running, st.LastError = false, truncate(p.err.Error(), 300)
		}
	default:
	}
	return st
}

// logWriter forwards the plugin's stdout/stderr to the log (rate limited).
func (p *plugin) logWriter(level slog.Level) *pluginLog { return &pluginLog{p: p, level: level} }

type pluginLog struct {
	p     *plugin
	level slog.Level
	buf   []byte
}

func (l *pluginLog) Write(b []byte) (int, error) {
	n := len(b)
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			l.p.logLine(l.level, line)
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 4096 { // an endless line
		l.p.logLine(l.level, string(l.buf))
		l.buf = l.buf[:0]
	}
	return n, nil
}

func (p *plugin) logLine(level slog.Level, msg string) {
	now := p.h.now().Unix() / 60
	p.mu.Lock()
	if p.logMinute != now {
		p.logMinute, p.logs = now, 0
	}
	p.logs++
	n := p.logs
	p.mu.Unlock()
	if n > 100 {
		return
	}
	p.h.log.Log(context.Background(), level, "plugin: "+truncate(msg, 1000), "plugin", p.pkg.Manifest.Name)
}

// State changes (admin).

// Info describes an installed plugin.
type Info struct {
	Package  *Package
	State    *db.Plugin // nil when never activated
	Status   Status
	Bundled  bool
	Approved bool     // the package's permissions are approved
	Extra    []string // permissions asked for beyond the approved ones
}

// List returns all installed plugins.
func (h *Host) List(ctx context.Context) ([]Info, error) {
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, pkg := range h.opts.Store.All() {
		name := pkg.Manifest.Name
		inf := Info{Package: pkg, State: states[name], Status: h.Status(name), Bundled: h.opts.Store.Bundled(name)}
		if inf.State != nil {
			approved, _ := ParsePermissions(inf.State.Approved)
			inf.Approved, inf.Extra = pkg.Manifest.Permissions.Covers(approved)
		} else {
			_, inf.Extra = pkg.Manifest.Permissions.Covers(Permissions{})
		}
		out = append(out, inf)
	}
	return out, nil
}

// Package returns the installed package name.
func (h *Host) Package(name string) *Package { return h.opts.Store.Get(name) }

// Activate approves the package's permissions and activates it. The
// caller has checked the administrator's re-authentication.
func (h *Host) Activate(ctx context.Context, name string) error {
	pkg := h.opts.Store.Get(name)
	if pkg == nil {
		return errors.New("no such plugin")
	}
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return err
	}
	st := states[name]
	if st == nil {
		st = &db.Plugin{Name: name}
	}
	st.Version, st.SHA256, st.Active, st.Approved, st.Updated = pkg.Manifest.Version, pkg.SHA256, true,
		pkg.Manifest.Permissions.Canonical(), h.now().Unix()
	if err := db.SavePlugin(ctx, h.opts.DB.W, st); err != nil {
		return err
	}
	if err := h.reload(ctx); err != nil {
		return err
	}
	h.opts.Site.Invalidate()
	h.action(ctx, name, "activate", nil)
	return nil
}

// Deactivate stops a plugin (its data stays).
func (h *Host) Deactivate(ctx context.Context, name string) error {
	h.action(ctx, name, "deactivate", nil)
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return err
	}
	if st := states[name]; st != nil {
		st.Active, st.Updated = false, h.now().Unix()
		if err := db.SavePlugin(ctx, h.opts.DB.W, st); err != nil {
			return err
		}
	}
	if err := h.reload(ctx); err != nil {
		return err
	}
	h.opts.Site.Invalidate()
	return nil
}

// Install stores an uploaded package. An active plugin whose new version
// asks for more permissions is deactivated until an administrator
// approves them; the returned list names the new permissions.
func (h *Host) Install(ctx context.Context, zip []byte) (*Package, []string, error) {
	pkg, err := h.opts.Store.Install(zip)
	if err != nil {
		return nil, nil, err
	}
	name := pkg.Manifest.Name
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return nil, nil, err
	}
	var extra []string
	if st := states[name]; st != nil {
		approved, _ := ParsePermissions(st.Approved)
		var ok bool
		if ok, extra = pkg.Manifest.Permissions.Covers(approved); !ok && st.Active {
			st.Active = false
		}
		st.Version, st.SHA256, st.Updated = pkg.Manifest.Version, pkg.SHA256, h.now().Unix()
		if err := db.SavePlugin(ctx, h.opts.DB.W, st); err != nil {
			return nil, nil, err
		}
	}
	if err := h.reload(ctx); err != nil {
		return nil, nil, err
	}
	h.opts.Site.Invalidate()
	return pkg, extra, nil
}

// Remove deletes an uploaded plugin (deactivating it unless a bundled
// version takes its place); data says whether its stored data goes too.
func (h *Host) Remove(ctx context.Context, name string, data bool) error {
	if err := h.opts.Store.Remove(name); err != nil {
		return err
	}
	err := h.opts.DB.Tx(ctx, func(tx *sql.Tx) error {
		if h.opts.Store.Get(name) == nil || data {
			return db.DeletePlugin(ctx, tx, name, data)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := h.reload(ctx); err != nil {
		return err
	}
	h.opts.Site.Invalidate()
	return nil
}

// action calls the plugin's action export; errors are logged.
func (h *Host) action(ctx context.Context, name, action string, data any) {
	p := h.get(name)
	if p == nil {
		return
	}
	raw, _ := json.Marshal(data)
	if err := p.call(ctx, ExportAction, callHook, ActionIn{Action: action, Data: raw}, nil); err != nil &&
		!errors.Is(err, ErrUnavailable) && !isNotImplemented(err) {
		h.log.Warn("plugin action", "plugin", name, "action", action, "err", err)
	}
}

// isNotImplemented reports the pdk's answer for exports a plugin does
// not implement.
func isNotImplemented(err error) bool {
	var pe *PluginError
	return errors.As(err, &pe) && pe.Msg == "not implemented"
}
