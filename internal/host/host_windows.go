package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"quailcrate/internal/diagnostic"
	"quailcrate/internal/gioui"
	"quailcrate/internal/pluginapps"
	"quailcrate/internal/pluginfiles"
	"quailcrate/internal/pluginio"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/theme"
	"quailcrate/internal/winshell"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

const (
	appearanceSetting = "appearance.config"
	appBoundsSetting  = "pluginapp.bounds"
)

type Host struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	db                  *store.Store
	log                 *slog.Logger
	ui                  *winui.UI
	optionDialog        pluginapps.DialogHandler
	confirmDialog       func(title, body string, done func(bool))
	manager             *plugins.Manager
	network             *pluginio.Service
	files               *pluginfiles.Service
	bundledDir, userDir string
	cacheDir            string
	pluginCatalog       plugins.Catalog

	lifecycle          sync.Mutex
	mu                 sync.Mutex
	records            map[string]*record
	issues             []winui.Plugin
	catalog            map[string]winshell.Entry
	apps               []v1.App
	detectedTools      []winshell.DetectedTool
	tools              []v1.Tool
	indexReady         bool
	query              string
	searchID           uint64
	visibleResults     []winui.Result
	eventRevision      uint64
	cancelSearch       context.CancelFunc
	hotkeyPending      bool
	hotkey             string
	locale             string
	dateSignal         chan struct{}
	appCatalog         pluginapps.Snapshot
	appChanged         chan struct{}
	relocalizing       bool
	scanned            bool
	launcherPreference string
	activeLauncher     winui.LauncherShell
	tabSequence        uint64
	tabs               map[string]*tabSession
}

type record struct {
	toolbox          *toolInstance
	blocked          bool
	grants           []string
	serviceSignature string
	serial           sync.Mutex
	pkg              plugins.Package
	info             winui.Plugin
	generation       uint64
	lifetime         context.Context
	cancel           context.CancelFunc
	registration     v1.Data
	widgets          []winui.Widget
}

var defaultPermissions = map[string][]string{
	"quailcrate.workbench":      nil,
	"quailcrate.launcher":       {"apps.read", "apps.launch"},
	"quailcrate.calendar":       nil,
	"quailcrate.desktop-groups": {"paths.open", "apps.read", "apps.launch", "urls.open", v2.Grant(v2.Items, "open")},
	"quailcrate.wallpaper":      {"wallpaper.set"},
}

func New(db *store.Store, logger *slog.Logger, bundledDir, userDir string, catalog *plugins.Catalog) *Host {
	ctx, cancel := context.WithCancel(context.Background())
	cacheDir := ""
	if userDir != "" {
		cacheDir = filepath.Join(filepath.Dir(userDir), "cache")
	}
	h := &Host{
		ctx: ctx, cancel: cancel, db: db, log: logger,
		manager: plugins.NewManager(), bundledDir: bundledDir, userDir: userDir, cacheDir: cacheDir,
		records: make(map[string]*record), locale: "en-US",
		dateSignal: make(chan struct{}, 1), appChanged: make(chan struct{}), tabs: make(map[string]*tabSession),
	}
	profile := filepath.Dir(userDir)
	h.network = pluginio.New(profile)
	h.files = pluginfiles.New(db, profile, bundledDir)
	if catalog != nil {
		h.pluginCatalog = *catalog
	}
	h.optionDialog = func(ctx context.Context, a v1.Action, done func(pluginapps.DialogResult)) {
		h.ui.PickOptionsFor(ctx, 0, a.Title, a.Options, false, func(ids []string, ok bool) { done(pluginapps.DialogResult{Items: ids, Canceled: !ok}) })
	}
	h.manager.SetNotifyHandler(func(id string, generation uint64, note v1.Notification) {
		go h.handleNativeNotify(id, generation, note)
	})
	return h
}

func isThirdPartyNative(pkg plugins.Package) bool {
	return !pkg.Bundled && pkg.Manifest.Runtime == "dll"
}

func (h *Host) Run() error {
	defer h.cancel()
	h.ui = winui.New(winui.Callbacks{
		ActivateTray:   h.activateTray,
		PluginApps:     h,
		Ready:          func() { go h.start() },
		Search:         h.search,
		OpenResult:     func(r winui.Result) { go h.activateResult(r, nil) },
		ResultAction:   func(r winui.Result, a winui.ResultMenuAction) { go h.activateResult(r, &a) },
		ChangeHotkey:   func(text string) { go h.changeHotkey(text) },
		ChooseLauncher: func(id string) { go h.chooseLauncher(id) },
		ChangeLanguage: func(preference string) { go h.changeLanguage(preference) },
		SaveAppearance: func(cfg theme.Config) { go h.saveAppearance(cfg) },
		SaveAppBounds:          func(bounds map[string]winui.AppWindowBounds) { go h.saveAppBounds(bounds) },
		TogglePlugin:           func(id string, enabled bool) { go h.toggle(id, enabled) },
		RevokePluginPermission: func(id, permission string) { go h.revokePermissionFromSettings(id, permission) },
		Rescan:                 func() { go h.scan() },
		OpenPluginDir: func() {
			go func() {
				if err := winshell.OpenPath(h.userDir); err != nil {
					h.showError("Could not open the plugin folder", err)
				}
			}()
		},
		WidgetEvent: func(id string, event v1.Event) { go h.event(id, event, event.Generation) },
		MoveWidget: func(id, widgetID string, generation uint64, layout winui.Layout) {
			go h.move(id, widgetID, generation, layout)
		},
		Command:     func(id, command string) { go h.command(id, command, nil) },
		SystemChanged: func() {
			select {
			case h.dateSignal <- struct{}{}:
			default:
			}
		},
		Exit: h.cancel,
	})
	err := h.ui.Run()
	h.cancel()
	h.stopAll()
	return err
}

func (h *Host) stopAll() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h.mu.Lock()
	var ids []string
	var closing []*toolInstance
	for id, rec := range h.records {
		if rec.generation != 0 {
			ids = append(ids, id)
			if tools := h.clearPluginLocked(rec); tools != nil {
				closing = append(closing, tools)
			}
		}
	}
	h.mu.Unlock()
	defer func() {
		for _, tools := range closing {
			<-tools.closed
		}
	}()
	done := make(chan struct{}, len(ids))
	for _, id := range ids {
		go func() {
			if err := h.manager.Disable(ctx, id); err != nil {
				h.log.Warn("stop plugin", "plugin", id, "error", diagnostic.English(err))
			}
			done <- struct{}{}
		}()
	}
	for range ids {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

func (h *Host) start() {
	preference, err := h.db.Setting(h.ctx, languageSetting, "system")
	if err != nil {
		h.log.Warn("read interface language", "error", diagnostic.English(err))
	}
	preference = configuredLanguage(preference)
	locale := effectiveLanguage(preference)
	h.mu.Lock()
	h.locale = locale
	h.mu.Unlock()
	h.ui.SetLanguage(preference, locale)
	if h.cacheDir != "" {
		gioui.SetFontCacheDir(filepath.Join(h.cacheDir, "font-cache"))
		h.ui.SetIconCacheDir(h.cacheDir)
	}
	h.refreshTools()
	rawAppearance, err := h.db.Setting(h.ctx, appearanceSetting, "")
	if err != nil {
		h.log.Warn("read appearance config", "error", diagnostic.English(err))
	}
	h.ui.SetAppearance(theme.ParseConfig(rawAppearance))
	rawAppBounds, err := h.db.Setting(h.ctx, appBoundsSetting, "")
	if err != nil {
		h.log.Warn("read plugin app bounds", "error", diagnostic.English(err))
	} else if rawAppBounds != "" {
		var saved map[string]winui.AppWindowBounds
		if err := json.Unmarshal([]byte(rawAppBounds), &saved); err == nil {
			h.ui.SetAppBounds(saved)
		}
	}
	launcherPreference, err := h.db.Setting(h.ctx, "launcher.provider", "quailcrate.launcher")
	if err != nil {
		h.log.Warn("read launcher provider", "error", diagnostic.English(err))
	}
	hotkey, err := h.db.Setting(h.ctx, "launcher.hotkey", "Alt+Space")
	if err != nil {
		h.log.Error("read hotkey setting", "error", diagnostic.English(err))
		hotkey = "Alt+Space"
	}
	h.mu.Lock()
	h.hotkey = hotkey
	h.launcherPreference = launcherPreference
	h.mu.Unlock()
	h.ui.RegisterHotkey(hotkey, func(err error) {
		if err != nil {
			h.log.Warn("register hotkey", "hotkey", hotkey, "error", diagnostic.English(err))
		}
	})
	go h.catalogLoop()
	go h.dateLoop()
	h.scan()
}

func (h *Host) chooseLauncher(id string) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	rec := h.records[id]
	available := rec != nil && rec.generation != 0 && rec.registration.Launcher != nil
	h.mu.Unlock()
	if !available {
		return
	}
	if err := h.db.SetSetting(h.ctx, "launcher.provider", id); err != nil {
		h.showError("Could not save launcher provider", err)
		return
	}
	h.mu.Lock()
	h.launcherPreference = id
	h.mu.Unlock()
	h.publish()
}

func (h *Host) saveAppearance(cfg theme.Config) {
	if h.ctx.Err() != nil {
		return
	}
	encoded, err := theme.MarshalConfig(cfg)
	if err != nil {
		h.log.Warn("encode appearance config", "error", diagnostic.English(err))
		return
	}
	if err := h.db.SetSetting(h.ctx, appearanceSetting, encoded); err != nil {
		h.showError("Could not save appearance settings", err)
	}
}

func (h *Host) saveAppBounds(bounds map[string]winui.AppWindowBounds) {
	if h.ctx.Err() != nil {
		return
	}
	encoded, err := json.Marshal(bounds)
	if err != nil {
		h.log.Warn("encode plugin app bounds", "error", diagnostic.English(err))
		return
	}
	if err := h.db.SetSetting(h.ctx, appBoundsSetting, string(encoded)); err != nil {
		h.log.Warn("save plugin app bounds", "error", diagnostic.English(err))
	}
}

func (h *Host) changeHotkey(text string) {
	h.mu.Lock()
	if h.hotkeyPending || h.ctx.Err() != nil {
		h.mu.Unlock()
		return
	}
	h.hotkeyPending = true
	previous := h.hotkey
	h.mu.Unlock()
	finish := func() {
		h.mu.Lock()
		h.hotkeyPending = false
		h.mu.Unlock()
	}
	h.ui.RegisterHotkey(text, func(err error) {
		if err != nil {
			finish()
			return
		}
		go func() {
			if err := h.db.SetSetting(h.ctx, "launcher.hotkey", text); err != nil {
				h.ui.RegisterHotkey(previous, func(error) { finish() })
				h.showError("Could not save the shortcut", err)
				return
			}
			h.mu.Lock()
			h.hotkey = text
			h.mu.Unlock()
			finish()
		}()
	})
}

func (h *Host) scan() {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.scanLocked()
}

func (h *Host) scanLocked() {
	if h.ctx.Err() != nil {
		return
	}
	found := dependencyOrder(h.pluginCatalog.Discover(h.bundledDir, h.userDir))
	present := make(map[string]bool)
	var issues []winui.Plugin
	for index, pkg := range found {
		id := pkg.Manifest.ID
		if pkg.Err != nil {
			name := pkg.Manifest.Name
			if name == "" {
				name = filepath.Base(pkg.Dir)
			}
			issues = append(issues, winui.Plugin{
				ID: fmt.Sprintf("invalid:%d", index), Name: name, Runtime: pkg.Manifest.Runtime,
				State: "failed", Status: "Load failed", Detail: diagnostic.English(pkg.Err),
			})
			h.log.Warn("reject plugin package", "plugin", id, "error", diagnostic.English(pkg.Err))
			continue
		}
		if present[id] {
			continue
		}
		present[id] = true
		h.mu.Lock()
		rec := h.records[id]
		if rec == nil {
			rec = &record{pkg: pkg, info: winui.Plugin{
				ID: id, Name: pkg.Manifest.Name, Runtime: pkg.Manifest.Runtime,
				CanToggle: true, State: "disabled",
			}}
			h.records[id] = rec
		}
		rec.info.Name = pkg.Manifest.DisplayName(h.locale)
		rec.info.Description = pkg.Manifest.DisplayDescription(h.locale)
		rec.info.Icon = pkg.Manifest.Icon
		rec.info.Bundled = pkg.Bundled
		rec.info.FullSystemAccess = isThirdPartyNative(pkg)
		active := rec.generation != 0
		changedGrants := active && !sameGrants(rec.grants, plugins.RequestedGrants(pkg.Manifest))
		changedBinary := active && isThirdPartyNative(pkg) && rec.pkg.NativeDigest != pkg.NativeDigest
		h.mu.Unlock()
		if changedGrants || changedBinary {
			h.disable(rec)
		} else if active {
			continue
		}
		h.mu.Lock()
		rec.pkg = pkg
		h.mu.Unlock()
		preference, err := h.db.Preference(h.ctx, id)
		if err != nil {
			h.fail(rec, err)
			continue
		}
		grants := preference.Grants
		requested := plugins.RequestedGrants(pkg.Manifest)
		enabled := preference.Enabled
		if !preference.Exists {
			defaults, listed := defaultPermissions[id]
			enabled = pkg.Bundled && pkg.Manifest.Runtime == "lua" && listed && sameGrants(defaults, requested)
			if enabled {
				grants = requested
			}
		} else if pkg.Bundled && !sameGrants(grants, requested) {
			defaults, listed := defaultPermissions[id]
			useDefaults := enabled && pkg.Manifest.Runtime == "lua" && listed && sameGrants(defaults, requested)
			// A bundled update can drop obsolete grants without changing the user's enable switch.
			alreadyGranted := !slices.ContainsFunc(requested, func(g string) bool { return !slices.Contains(grants, g) })
			if useDefaults || alreadyGranted {
				grants = requested
				if err := h.db.SetPreference(h.ctx, id, enabled, grants); err != nil {
					h.fail(rec, err)
					continue
				}
			}
		}
		if enabled && isThirdPartyNative(pkg) {
			trust, err := h.db.NativeTrust(h.ctx, id)
			if err != nil {
				h.fail(rec, err)
				continue
			}
			if !trust.Exists || trust.SHA256 != pkg.NativeDigest {
				h.mu.Lock()
				rec.info.RequestedPermissions = slices.Clone(requested)
				rec.info.GrantedPermissions = slices.Clone(grants)
				h.mu.Unlock()
				h.setStatus(rec, false, "authorization", "Native plugin binary has changed or has not been trusted. Enable it again to review full system access.")
				continue
			}
		}
		if enabled && !sameGrants(grants, requested) {
			h.mu.Lock()
			rec.info.RequestedPermissions = slices.Clone(requested)
			rec.info.GrantedPermissions = slices.Clone(grants)
			h.mu.Unlock()
			h.setStatus(rec, false, "authorization", "The plugin's requested permissions have changed.")
			continue
		}
		h.mu.Lock()
		rec.info.RequestedPermissions = slices.Clone(requested)
		rec.info.GrantedPermissions = slices.Clone(grants)
		h.mu.Unlock()
		if enabled {
			h.enable(rec, grants)
		} else {
			h.setStatus(rec, false, "disabled", "")
		}
	}
	h.mu.Lock()
	h.issues = issues
	var removed []*record
	for id, rec := range h.records {
		if !present[id] {
			removed = append(removed, rec)
		}
	}
	h.mu.Unlock()
	for _, rec := range removed {
		h.disable(rec)
		h.mu.Lock()
		delete(h.records, rec.info.ID)
		h.mu.Unlock()
	}
	h.mu.Lock()
	h.scanned = true
	h.mu.Unlock()
	h.reconcileServices()
	h.publish()
	h.repeatSearch()
}

func sameGrants(left, right []string) bool {
	a, b := slices.Clone(left), slices.Clone(right)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func formatGrantDescriptions(locale string, grants []string) string {
	lines := make([]string, 0, len(grants))
	for _, g := range grants {
		var desc string
		switch g {
		case "network.http":
			desc = text(locale, "Public internet access", "公开互联网访问")
		case "network.local":
			desc = text(locale, "Localhost and LAN network access", "本机与局域网访问")
		case "audio.play":
			desc = text(locale, "Audio playback", "音频播放")
		case "files.directory":
			desc = text(locale, "Authorized folder access", "授权目录访问")
		case "apps.read":
			desc = text(locale, "Read installed app list", "读取已安装应用列表")
		case "apps.launch":
			desc = text(locale, "Launch applications", "启动应用程序")
		case "paths.open":
			desc = text(locale, "Open files and folders", "打开文件与目录")
		case "urls.open":
			desc = text(locale, "Open external web links", "打开外部网页链接")
		case "wallpaper.set":
			desc = text(locale, "Change desktop wallpaper", "设置桌面壁纸")
		}
		if desc != "" {
			lines = append(lines, "• "+desc+" ("+g+")")
		} else {
			lines = append(lines, "• "+g)
		}
	}
	return strings.Join(lines, "\n")
}

func (h *Host) revokePermissionFromSettings(id, permission string) {
	if err := h.RevokePluginPermission(id, permission); err != nil && !errors.Is(err, ErrInvocationUnavailable) {
		h.showError("Could not revoke plugin permission", err)
	}
}

func formatNativeRiskWarning(locale, entrySHA256 string, binaryChanged bool, grants []string) string {
	var b strings.Builder
	if binaryChanged {
		b.WriteString(text(locale,
			"Warning: This native plugin's binary files have changed since you last trusted it.\n\n",
			"注意：该原生插件的二进制文件自上次信任后已发生变化。\n\n"))
	}
	b.WriteString(text(locale,
		"This is a third-party native DLL plugin. It runs outside the sandbox with your full Windows user permissions (unrestricted local files, network, system APIs, and child processes).\n\nOnly enable native plugins from authors you trust. Any data loss or system impact is at your own risk.",
		"该插件为第三方原生二进制组件（DLL），将以当前系统用户的完全权限运行，不受沙箱限制，可自由读写本地文件、访问网络、调用系统接口及启动外部程序。\n\n请仅启用来自可信作者的插件；因第三方原生插件导致的数据损坏或系统异常由插件作者及用户自行承担。"))
	if len(entrySHA256) >= 16 {
		b.WriteString("\n\nSHA-256: ")
		b.WriteString(entrySHA256[:16])
		b.WriteString("...")
	}
	if len(grants) > 0 {
		b.WriteString(text(locale,
			"\n\nDeclared host shell capabilities:\n",
			"\n\n同时声明的宿主壳层能力：\n"))
		b.WriteString(formatGrantDescriptions(locale, grants))
	}
	return b.String()
}

func (h *Host) confirm(title, body string, done func(bool)) {
	if h.confirmDialog != nil {
		h.confirmDialog(title, body, done)
		return
	}
	h.ui.Confirm(title, body, done)
}

func (h *Host) toggle(id string, enabled bool) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return
	}
	preference, err := h.db.Preference(h.ctx, id)
	if err != nil {
		h.showError("Could not read plugin permissions", err)
		return
	}
	grants := preference.Grants
	h.mu.Lock()
	locale := h.locale
	name := rec.pkg.Manifest.DisplayName(locale)
	pkg := rec.pkg
	h.mu.Unlock()
	requested := plugins.RequestedGrants(pkg.Manifest)
	if enabled && isThirdPartyNative(pkg) {
		entrySHA256, nativeDigest, hashErr := plugins.ComputeNativeDigest(pkg.Dir, pkg.Entry)
		if hashErr != nil {
			h.fail(rec, hashErr)
			return
		}
		trust, trustErr := h.db.NativeTrust(h.ctx, id)
		if trustErr != nil {
			h.showError("Could not read native plugin trust record", trustErr)
			return
		}
		if !trust.Exists || trust.SHA256 != nativeDigest || !sameGrants(grants, requested) {
			grants = slices.Clone(requested)
			answer := make(chan bool, 1)
			h.confirm(
				text(locale, "Warning: Enable Native Plugin "+name, "高危提醒：启用原生插件「"+name+"」"),
				formatNativeRiskWarning(locale, entrySHA256, trust.Exists && trust.SHA256 != nativeDigest, grants),
				func(ok bool) { answer <- ok },
			)
			select {
			case approved := <-answer:
				if !approved {
					h.setStatus(rec, false, "disabled", text(locale,
						"Native plugin full-access confirmation was declined. Toggle on again to review and enable.",
						"已取消第三方原生插件的完全权限确认；重新启用时可再次查看并确认。"))
					return
				}
			case <-h.ctx.Done():
				return
			}
			if err := h.db.SetNativeTrust(h.ctx, id, nativeDigest, time.Now().Unix()); err != nil {
				h.showError("Could not save native plugin trust record", err)
				return
			}
		}
		h.mu.Lock()
		rec.pkg.EntrySHA256 = entrySHA256
		rec.pkg.NativeDigest = nativeDigest
		h.mu.Unlock()
	} else {
		if enabled && !sameGrants(grants, requested) {
			grants = slices.Clone(requested)
			answer := make(chan bool, 1)
			h.confirm(text(locale, "Enable "+name, "启用「"+name+"」"),
				text(locale, "Confirm that you trust this plugin and allow the following permissions:\n",
					"请确认信任此插件，并允许以下能力：\n")+formatGrantDescriptions(locale, grants), func(ok bool) { answer <- ok })
			select {
			case approved := <-answer:
				if !approved {
					h.setStatus(rec, false, "disabled", text(locale,
						"Permission request was declined. Toggle on again to review and grant permissions.",
						"已拒绝授权请求；重新启用时可再次查看并授予权限。"))
					return
				}
			case <-h.ctx.Done():
				return
			}
		}
		if enabled && !pkg.Bundled && !preference.Exists && len(grants) == 0 {
			answer := make(chan bool, 1)
			h.confirm(text(locale, "Enable "+name, "启用「"+name+"」"),
				text(locale, "This local plugin does not request extra permissions.", "此插件由本地目录加载，未声明额外权限。"),
				func(ok bool) { answer <- ok })
			select {
			case approved := <-answer:
				if !approved {
					return
				}
			case <-h.ctx.Done():
				return
			}
		}
	}
	if err := h.db.SetPreference(h.ctx, id, enabled, grants); err != nil {
		h.showError("Could not save plugin settings", err)
		return
	}
	h.mu.Lock()
	rec.info.RequestedPermissions = slices.Clone(requested)
	rec.info.GrantedPermissions = slices.Clone(grants)
	h.mu.Unlock()
	if enabled {
		h.enable(rec, grants)
	} else {
		h.disable(rec)
	}
	h.reconcileServices()
	h.publish()
	h.repeatSearch()
}

func (h *Host) enable(rec *record, grants []string) {
	rec.serial.Lock()
	defer rec.serial.Unlock()
	id := rec.pkg.Manifest.ID
	if h.manager.Generation(id) != 0 {
		return
	}
	h.setStatus(rec, true, "loading", "")
	rec.grants = slices.Clone(grants)
	rec.blocked = false
	if err := h.requiredServices(rec.pkg.Manifest); err != nil {
		rec.blocked = true
		h.fail(rec, err)
		return
	}
	started := time.Now()
	snapshot, err := h.db.Snapshot(h.ctx, id)
	if err != nil {
		h.fail(rec, err)
		return
	}
	pkg := rec.pkg
	if dir := h.pluginDataDir(id); dir != "" {
		pkg.DataDir = dir
	}
	response, err := h.manager.Enable(h.ctx, pkg, grants, h.payload(snapshot))
	if err != nil {
		h.fail(rec, err)
		return
	}
	generation := response.Generation
	changes := store.Mutations{Initialize: true, State: response.Data.State}
	if !snapshot.Initialized {
		for _, definition := range response.Data.Widgets {
			if definition.Default {
				changes.Create = append(changes.Create, newWidget(definition.ID, definition))
			}
		}
	}
	if err := h.db.Apply(h.ctx, id, changes); err != nil {
		_ = h.manager.Disable(h.ctx, id)
		h.fail(rec, err)
		return
	}
	h.mu.Lock()
	rec.registration = response.Data
	rec.lifetime, rec.cancel = context.WithCancel(h.ctx)
	rec.generation = generation
	if rec.pkg.Manifest.HostAPIVersion == 2 {
		rec.toolbox = h.newToolInstance(rec.lifetime, id, generation, grants)
	}
	rec.info.Enabled = true
	rec.info.State = "running"
	rec.info.Detail = ""
	h.mu.Unlock()
	if rec.pkg.Manifest.Runtime == "dll" {
		go h.watchNativeExit(rec, generation, h.manager.GenerationDone(id, generation))
	}
	h.log.Info("plugin enabled", "plugin", id, "duration", time.Since(started))
	h.refresh(rec, generation)
}

func (h *Host) watchNativeExit(rec *record, generation uint64, done <-chan struct{}) {
	select {
	case <-h.ctx.Done():
		return
	case <-done:
	}
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	if rec.generation != generation || h.ctx.Err() != nil {
		h.mu.Unlock()
		return
	}
	tools := h.clearPluginLocked(rec)
	h.mu.Unlock()
	if tools != nil {
		<-tools.closed
	}
	h.fail(rec, errors.New("native plugin worker stopped"))
	h.reconcileServices()
	h.repeatSearch()
}

func (h *Host) handleNativeNotify(id string, generation uint64, note v1.Notification) {
	if h.ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return
	}
	rec.serial.Lock()
	defer rec.serial.Unlock()
	h.mu.Lock()
	if h.ctx.Err() != nil || rec.generation != generation || h.records[id] != rec {
		h.mu.Unlock()
		return
	}
	hasWidgets := len(rec.registration.Widgets) > 0
	h.mu.Unlock()

	if len(note.State) != 0 {
		h.mu.Lock()
		if h.ctx.Err() != nil || rec.generation != generation || h.records[id] != rec {
			h.mu.Unlock()
			return
		}
		err := h.db.Apply(h.ctx, id, store.Mutations{State: note.State})
		h.mu.Unlock()
		if err != nil {
			h.log.Warn("native notify state commit", "plugin", id, "error", diagnostic.English(err))
			return
		}
	}

	invalidateTab := func(tabID string) {
		h.mu.Lock()
		tab := h.tabs[id]
		if tab != nil && tab.ref.Generation == generation && (tabID == "" || tab.ref.TabID == tabID) {
			tab.view = nil
			tab.pageReady = false
			if !h.relocalizing {
				h.notifyAppsLocked()
			}
		}
		h.mu.Unlock()
	}

	switch note.Type {
	case "widget.refresh":
		if len(note.State) != 0 {
			invalidateTab("")
		}
		h.refresh(rec, generation)
	case "page.notify":
		invalidateTab(note.TabID)
		if len(note.State) != 0 && hasWidgets {
			h.refresh(rec, generation)
		}
	case "event.publish":
		args := note.Args
		if len(args) == 0 {
			args = note.Detail
		}
		h.publishEvent(id, generation, v1.Action{Type: "event.publish", Topic: note.Topic, Args: args}, nil)
	case "state.set":
		invalidateTab("")
		if hasWidgets {
			h.refresh(rec, generation)
		}
	}
}

func (h *Host) clearPluginLocked(rec *record) *toolInstance {
	id := rec.info.ID
	if tab := h.tabs[id]; tab != nil {
		tab.cancel()
		delete(h.tabs, id)
	}
	rec.generation = 0
	if rec.cancel != nil {
		rec.cancel()
	}
	tools := rec.toolbox
	rec.toolbox = nil
	rec.widgets = nil
	rec.registration = v1.Data{}
	return tools
}

func (h *Host) disable(rec *record) {
	rec.blocked = false
	id := rec.info.ID
	h.mu.Lock()
	tools := h.clearPluginLocked(rec)
	h.mu.Unlock()
	if tools != nil {
		<-tools.closed
	}
	h.setStatus(rec, false, "stopping", "")
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
	defer cancel()
	if err := h.manager.Disable(ctx, id); err != nil && h.ctx.Err() == nil {
		h.setStatus(rec, false, "stopping", diagnostic.English(err))
		h.log.Warn("disable plugin", "plugin", id, "error", diagnostic.English(err))
		return
	}
	h.setStatus(rec, false, "disabled", "")
}

func (h *Host) setStatus(rec *record, enabled bool, state, detail string) {
	h.mu.Lock()
	rec.info.Enabled, rec.info.State, rec.info.Detail = enabled, state, detail
	h.mu.Unlock()
	h.publish()
}

func (h *Host) fail(rec *record, err error) {
	h.log.Error("initialize plugin", "plugin", rec.info.ID, "error", diagnostic.English(err))
	h.setStatus(rec, true, "failed", diagnostic.English(err))
}

func (h *Host) publish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	var widgets []winui.Widget
	var tray []winui.TrayEntry
	var shells []winui.LauncherShell
	var settings *v2.Settings
	var registrations []pluginapps.Registration
	status := slices.Clone(h.issues)
	ids := make([]string, 0, len(h.records))
	for id := range h.records {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		rec := h.records[id]
		info := rec.info
		info.Status = localizedStatus(h.locale, info.State)
		status = append(status, info)
		if rec.generation == 0 {
			continue
		}
		widgets = append(widgets, rec.widgets...)
		for _, item := range rec.registration.Tray {
			tray = append(tray, winui.TrayEntry{PluginID: id, Generation: rec.generation, Item: item})
		}
		if page := rec.registration.Settings; page != nil && (settings == nil || id == "quailcrate.workbench") {
			settings = page
		}
		if shell := rec.registration.Launcher; shell != nil {
			shells = append(shells, winui.LauncherShell{PluginID: id, Name: info.Name, View: shell})
		}
		registrations = append(registrations, pluginapps.Registration{
			PluginID: id, PluginName: info.Name, Generation: rec.generation,
			Tabs: rec.registration.AppTabs, Commands: rec.registration.Commands,
		})
	}
	catalog := pluginapps.Build(registrations)
	if !h.relocalizing {
		h.ui.SetTray(tray)
		if h.scanned {
			h.ui.SetSettingsDefinition(settings)
			active := winui.ResolveLauncher(shells, h.launcherPreference)
			if active.View != h.activeLauncher.View {
				h.activeLauncher = active
				h.consumeSearchLocked()
			}
			h.ui.SetLaunchers(shells, active)
		}
		h.appCatalog = catalog
		h.notifyAppsLocked()
	}
	var commands []winui.Command
	for _, cmd := range catalog.Commands {
		commands = append(commands, winui.Command{PluginID: cmd.PluginID, ID: cmd.ID, Title: cmd.Title, Location: cmd.Location})
	}
	h.ui.SetPlugins(status)
	h.ui.SetCommands(commands)
	h.ui.SetWidgets(widgets)
}

func (h *Host) activateTray(entry winui.TrayEntry) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := h.records[entry.PluginID]
	if entry.Generation == 0 || rec == nil || rec.generation != entry.Generation ||
		entry.Item.Separator || !slices.Contains(rec.registration.Tray, entry.Item) {
		return false
	}
	if entry.Item.Intent == "command" {
		go h.invokeAndReport(entry.PluginID, entry.Generation, "command", v1.Payload{CommandID: entry.Item.Command})
	}
	return true
}

func (h *Host) usable(id string, generation uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := h.records[id]
	return rec != nil && rec.generation != 0 && (generation == 0 || rec.generation == generation)
}

func (h *Host) refreshTools() {
	detected := winshell.DetectTools()
	list := make([]v1.Tool, 0, len(detected))
	for _, item := range detected {
		list = append(list, item.Tool)
	}
	h.mu.Lock()
	h.detectedTools = detected
	h.tools = list
	h.mu.Unlock()
}

func (h *Host) payload(snapshot store.Snapshot) v1.Payload {
	h.mu.Lock()
	apps, tools, locale := h.apps, h.tools, h.locale
	var services []v2.Available
	for id, r := range h.records {
		if r.generation != 0 {
			for _, e := range r.pkg.Manifest.Provides {
				services = append(services, v2.Available{Provider: id, Export: e})
			}
		}
	}
	h.mu.Unlock()
	instances := make([]v1.WidgetInstance, 0, len(snapshot.Widgets))
	for _, w := range snapshot.Widgets {
		instances = append(instances, v1.WidgetInstance{ID: w.ID, Type: w.Type})
	}
	return v1.Payload{State: snapshot.State, Widgets: instances, Apps: apps, Tools: tools, Date: localDate(), UnixTime: time.Now().Unix(), Locale: locale, Services: services}
}

func localDate() string { return time.Now().Format("2006-01-02") }

func (h *Host) dateLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := localDate()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
		case <-h.dateSignal:
		}
		now := localDate()
		if now != last {
			last = now
			h.broadcast("date.changed")
		}
	}
}

func (h *Host) catalogLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		entries, err := winshell.Enumerate()
		if err != nil {
			h.log.Warn("enumerate Windows apps", "error", diagnostic.English(err))
		}
		index := make(map[string]winshell.Entry, len(entries))
		apps := make([]v1.App, 0, len(entries))
		for _, entry := range entries {
			index[entry.App.ID] = entry
			apps = append(apps, entry.App)
		}
		h.mu.Lock()
		changed := !h.indexReady || !slices.Equal(apps, h.apps)
		h.catalog, h.apps, h.indexReady = index, apps, true
		h.mu.Unlock()
		if changed {
			h.log.Info("Windows app catalog ready", "count", len(entries))
			h.broadcast("apps.changed")
			h.repeatSearch()
		}
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Host) broadcast(eventType string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, rec := range h.records {
		if rec.generation != 0 && slices.Contains(rec.registration.Subscribe, eventType) {
			go h.event(id, v1.Event{Type: eventType}, rec.generation)
		}
	}
}

func (h *Host) command(id, commandID string, args json.RawMessage) {
	h.invokeAndReport(id, 0, "command", v1.Payload{CommandID: commandID, Args: args})
}

func (h *Host) event(id string, event v1.Event, generation uint64) {
	if event.Type == "widget.drop" && len(event.Paths) > 0 {
		event.Paths, event.Imported = h.extractURLShortcuts(event.Paths, event.Imported)
	}
	h.invokeAndReport(id, generation, "event", v1.Payload{Event: &event, WidgetID: event.WidgetID})
}

// extractURLShortcuts parses any dropped/picked Windows .url shortcuts into standalone entries
// with locally cached icons so users can safely delete the original desktop .url files.
func (h *Host) extractURLShortcuts(paths []string, existing []v1.ImportedShortcut) ([]string, []v1.ImportedShortcut) {
	kept := make([]string, 0, len(paths))
	imported := existing
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".url") {
			if item, err := winshell.ImportURLShortcut(p, h.cacheDir); err == nil {
				imported = append(imported, item)
				continue
			}
		}
		kept = append(kept, p)
	}
	return kept, imported
}

func (h *Host) invokeAndReport(id string, generation uint64, op string, request v1.Payload) {
	err := h.invoke(id, generation, op, request)
	if err != nil && !errors.Is(err, ErrInvocationUnavailable) && !errors.Is(err, context.Canceled) {
		h.reportCall(id, op, err)
	}
}

func (h *Host) invoke(id string, wantedGeneration uint64, op string, request v1.Payload) error {
	return h.invokeFrom(id, wantedGeneration, op, request, nil, 0)
}

func (h *Host) invokeFrom(id string, wantedGeneration uint64, op string, request v1.Payload, origin *tabSession, revision uint64) error {
	return h.invokeScoped(id, wantedGeneration, op, request, origin, revision, nil)
}

func (h *Host) invokeScoped(id string, wantedGeneration uint64, op string, request v1.Payload, origin *tabSession, revision uint64, scope *callScope) error {
	if err := h.ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return ErrInvocationUnavailable
	}
	rec.serial.Lock()
	defer rec.serial.Unlock()
	h.mu.Lock()
	generation := rec.generation
	registration := rec.registration
	toolbox := rec.toolbox
	if e := request.Event; e != nil && (e.ViewRevision != 0 || e.Type == "widget.action" && len(e.InputValue) != 0) && !slices.ContainsFunc(rec.widgets, func(w winui.Widget) bool {
		return w.ID == e.WidgetID && w.Revision == e.ViewRevision && w.Generation == e.Generation && (len(e.InputValue) == 0 || w.View.AllowsInput(e.ControlID, e.InputValue))
	}) {
		h.mu.Unlock()
		return ErrInvocationUnavailable
	}
	currentTab := h.currentTabLocked(origin)
	if currentTab && origin != nil && request.Event != nil {
		currentTab = origin.allowsEvent(revision, request.Event)
	}
	h.mu.Unlock()
	if !currentTab || generation == 0 || (wantedGeneration != 0 && wantedGeneration != generation) {
		return ErrInvocationUnavailable
	}
	snapshot, err := h.db.Snapshot(h.ctx, id)
	if err != nil {
		return err
	}
	payload := h.payload(snapshot)
	payload.CommandID, payload.Args, payload.Event, payload.WidgetID = request.CommandID, request.Args, request.Event, request.WidgetID
	payload.TabID = request.TabID
	payload.Service, payload.Method, payload.ServiceVersion, payload.Caller = request.Service, request.Method, request.ServiceVersion, request.Caller
	if payload.WidgetID != "" {
		index := slices.IndexFunc(snapshot.Widgets, func(widget store.Widget) bool { return widget.ID == payload.WidgetID })
		if index < 0 {
			return ErrInvocationUnavailable
		}
		payload.WidgetType = snapshot.Widgets[index].Type
	}
	started := time.Now()
	ctx := h.ctx
	if origin != nil {
		ctx = origin.ctx
	}
	if scope != nil {
		ctx = scope.ctx
	}
	response, err := h.manager.CallGeneration(ctx, id, generation, op, payload)
	if err != nil {
		if !h.usable(id, generation) || (origin != nil && origin.ctx.Err() != nil) {
			return ErrInvocationUnavailable
		}
		return err
	}
	if response.Generation != generation {
		return ErrInvocationUnavailable
	}
	changes := store.Mutations{State: response.Data.State}
	refresh := len(changes.State) != 0 || (op == "event" && request.Event.Type == "date.changed")
	var deferred []v1.Action
	if scope != nil && len(scope.chain) > 8 && slices.ContainsFunc(response.Data.Actions, func(a v1.Action) bool { return a.Type == "service.call" }) {
		return errors.New("service continuation exceeds call limit")
	}
	if scope != nil && !scope.interactive && slices.ContainsFunc(response.Data.Actions, func(a v1.Action) bool { return strings.HasPrefix(a.Type, "ui.") }) {
		return errors.New("service dialog requires a user interaction")
	}
	if op == "service" {
		if scope == nil {
			return ErrInvocationUnavailable
		}
		scope.reply = response.Data.Reply
		if scope.reply == nil {
			if !scope.interactive || len(response.Data.State) > 0 || len(response.Data.Actions) != 1 || response.Data.Actions[0].Type != "ui.pickOptions" {
				return errors.New("service must return a terminal reply or an interactive option picker")
			}
			scope.picker = &response.Data.Actions[0]
			return nil
		}
	}
	for _, action := range response.Data.Actions {
		switch action.Type {
		case "widget.create":
			definition, ok := widgetDefinition(registration, action.WidgetType)
			if !ok {
				return fmt.Errorf("unknown widget type %q", action.WidgetType)
			}
			changes.Create = append(changes.Create, newWidget(action.WidgetID, definition))
			refresh = true
		case "widget.delete":
			changes.Delete = append(changes.Delete, action.WidgetID)
			refresh = true
		case "widget.refresh":
			refresh = true
		default:
			if origin != nil && origin.dialogs == nil && strings.HasPrefix(action.Type, "ui.") {
				return errors.New("the originating tab has no dialog handler")
			}
			deferred = append(deferred, action)
		}
	}
	if len(changes.State)+len(changes.Create)+len(changes.Delete) != 0 {
		h.mu.Lock()
		if ctx.Err() != nil || rec.generation != generation || h.records[id] != rec || !h.currentTabLocked(origin) {
			h.mu.Unlock()
			return ErrInvocationUnavailable
		}
		err := h.db.Apply(h.ctx, id, changes)
		h.mu.Unlock()
		if err != nil {
			return err
		}
	}
	h.log.Debug("plugin call", "plugin", id, "operation", op, "duration", time.Since(started))
	// Events and commands can change either persisted or Lua-local state.
	// Reject clicks from the previous frame before another call takes serial.
	h.mu.Lock()
	tab := h.tabs[id]
	if tab != nil && tab.ref.Generation == generation {
		tab.view = nil
		tab.pageReady = false
		if !refresh && !h.relocalizing {
			h.notifyAppsLocked()
		}
	}
	h.mu.Unlock()
	if scope != nil && request.Event != nil && request.Event.Type == "page.message" {
		scope.pageReply = slices.Clone(response.Data.PageReply)
	}
	if refresh {
		h.refresh(rec, generation)
	}
	if len(deferred) != 0 {
		interactive := op == "command" || (request.Event != nil && (strings.HasPrefix(request.Event.Type, "widget.") || request.Event.Type == "tab.action" || (request.Event.Type == "action.result" && request.Event.Status == "")))
		var chain []string
		if scope != nil {
			interactive = scope.interactive
			chain = scope.chain
		}
		dispatch := func() {
			h.perform(id, generation, payload.WidgetID, request.Event, deferred, origin, interactive, chain)
		}
		if toolbox != nil && slices.ContainsFunc(deferred, func(a v1.Action) bool { _, ok := v2.ToolGrant(a.Type); return ok }) {
			previous := toolbox.lastDispatch
			done := make(chan struct{})
			toolbox.lastDispatch = done
			go func() {
				defer close(done)
				if previous != nil {
					select {
					case <-previous:
					case <-toolbox.ctx.Done():
						return
					}
				}
				dispatch()
			}()
		} else {
			go dispatch()
		}
	}
	return nil
}

func newWidget(id string, definition v1.WidgetDefinition) store.Widget {
	return store.Widget{ID: id, Type: definition.ID, Width: definition.Width, Height: definition.Height}
}

func widgetDefinition(registration v1.Data, id string) (v1.WidgetDefinition, bool) {
	index := slices.IndexFunc(registration.Widgets, func(definition v1.WidgetDefinition) bool { return definition.ID == id })
	if index < 0 {
		return v1.WidgetDefinition{}, false
	}
	return registration.Widgets[index], true
}

func (h *Host) refresh(rec *record, generation uint64) {
	id := rec.info.ID
	snapshot, err := h.db.Snapshot(h.ctx, id)
	if err != nil {
		h.log.Warn("read widgets", "plugin", id, "error", diagnostic.English(err))
		return
	}
	base := h.payload(snapshot)
	h.mu.Lock()
	registration := rec.registration
	previous := slices.Clone(rec.widgets)
	h.mu.Unlock()
	widgets := make([]winui.Widget, 0, len(snapshot.Widgets))
	for _, widget := range snapshot.Widgets {
		if !h.usable(id, generation) {
			return
		}
		definition, known := widgetDefinition(registration, widget.Type)
		if !known {
			continue
		}
		request := base
		request.WidgetID, request.WidgetType = widget.ID, widget.Type
		prev := slices.IndexFunc(previous, func(old winui.Widget) bool {
			return old.ID == widget.ID && old.Generation == generation
		})
		response, err := h.manager.Call(h.ctx, id, "widget_state", request)
		if err != nil {
			h.log.Warn("read widget view", "plugin", id, "widget", widget.ID, "error", diagnostic.English(err))
			if prev >= 0 {
				widgets = append(widgets, previous[prev])
			}
			continue
		}
		if response.Data.View != nil {
			revision := uint64(1)
			var layoutRevision uint64
			if prev >= 0 {
				old := previous[prev]
				layoutRevision = old.Layout.Revision
				revision = old.Revision
				if !reflect.DeepEqual(old.View, *response.Data.View) {
					revision++
				}
			}
			widgets = append(widgets, winui.Widget{
				Generation: generation, Revision: revision,
				PluginID: id, ID: widget.ID, View: *response.Data.View,
				Layout: winui.Layout{
					Revision: layoutRevision,
					Monitor: widget.Monitor, X: widget.X, Y: widget.Y, Width: widget.Width,
					Height: widget.Height, Placed: widget.Placed, Anchor: definition.Anchor,
					Collapsed: widget.Collapsed, Locked: widget.Locked,
					AlwaysOnTop: widget.AlwaysOnTop, ExpandedHeight: widget.ExpandedHeight,
					IconSize: widget.IconSize,
				},
			})
		}
	}
	h.mu.Lock()
	if rec.generation == generation {
		rec.widgets = widgets
	}
	h.mu.Unlock()
	h.publish()
}

func (h *Host) move(id, widgetID string, generation uint64, p winui.Layout) {
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return
	}
	rec.serial.Lock()
	defer rec.serial.Unlock()
	h.mu.Lock()
	if generation == 0 || rec.generation != generation || h.records[id] != rec {
		h.mu.Unlock()
		return
	}
	i := slices.IndexFunc(rec.widgets, func(w winui.Widget) bool { return w.ID == widgetID })
	if i < 0 || p.Revision <= rec.widgets[i].Layout.Revision {
		h.mu.Unlock()
		return
	}
	ctx, locale := rec.lifetime, h.locale
	h.mu.Unlock()
	if err := h.db.Move(ctx, id, store.Widget{
		ID: widgetID, Monitor: p.Monitor, X: p.X, Y: p.Y, Width: p.Width, Height: p.Height, Placed: p.Placed,
		Collapsed: p.Collapsed, Locked: p.Locked, AlwaysOnTop: p.AlwaysOnTop, ExpandedHeight: p.ExpandedHeight,
		IconSize: p.IconSize,
	}); err != nil {
		if ctx.Err() == nil {
			h.showError(text(locale, "Could not save the widget layout", "无法保存组件布局"), err)
		}
		return
	}
	h.mu.Lock()
	if rec.generation == generation {
		rec.widgets[i].Layout = p
	}
	h.mu.Unlock()
}

func (h *Host) perform(id string, generation uint64, widgetID string, previous *v1.Event, actions []v1.Action, origin *tabSession, interactive bool, chain []string) {
	for _, action := range actions {
		if !h.usable(id, generation) || h.ctx.Err() != nil {
			return
		}
		if _, ok := v2.ToolGrant(action.Type); ok {
			h.performTool(id, generation, action, origin, interactive)
			continue
		}
		if origin != nil && origin.ctx.Err() != nil {
			continue
		}
		event := v1.Event{Type: "action.result", WidgetID: widgetID, ActionID: action.ID, Previous: previous}
		if origin != nil {
			event.TabID = origin.ref.TabID
			if strings.HasPrefix(action.Type, "ui.") {
				h.presentTabDialog(origin, action, event)
				continue
			}
		}
		if action.WidgetID != "" {
			event.WidgetID = action.WidgetID
		}
		var err error
		switch action.Type {
		case "service.call":
			h.callService(id, generation, action, origin, widgetID, interactive, chain)
			continue
		case "event.publish":
			h.publishEvent(id, generation, action, previous)
			continue
		case "ui.promptText":
			h.ui.PromptText(action.Title, action.Value, func(text string, ok bool) {
				event.Value, event.Canceled = text, !ok
				go h.event(id, event, generation)
			})
			continue
		case "ui.confirm":
			body := action.Body
			if body == "" {
				body = action.Value
			}
			h.ui.Confirm(action.Title, body, func(ok bool) {
				event.Canceled = !ok
				if ok {
					event.Value = "ok"
				}
				go h.event(id, event, generation)
			})
			continue
		case "ui.pickOptions":
			h.mu.Lock()
			rec := h.records[id]
			if rec == nil || rec.generation != generation {
				h.mu.Unlock()
				return
			}
			lifetime := rec.lifetime
			h.mu.Unlock()
			h.ui.PickOptionsFor(lifetime, 0, action.Title, action.Options, action.Multiple, func(selected []string, ok bool) {
				event.Items, event.Canceled = selected, !ok
				go h.event(id, event, generation)
			})
			continue
		case "ui.pickPaths":
			h.ui.PickPaths(action.Title, action.Extensions, action.AllowDirectories, action.DirectoriesOnly, action.Multiple, func(paths []string, ok bool) {
				if ok && len(paths) > 0 {
					event.Paths, event.Imported = h.extractURLShortcuts(paths, nil)
				} else {
					event.Paths = paths
				}
				event.Canceled = !ok
				go h.event(id, event, generation)
			})
			continue
		case "ui.message":
			h.ui.Message(action.Title, action.Value)
			continue
		case "apps.launch":
			h.mu.Lock()
			entry, found := h.catalog[action.AppID]
			h.mu.Unlock()
			if !found {
				err = errors.New("The application is no longer in the catalog.")
			} else {
				err = winshell.Launch(entry)
			}
		case "paths.open":
			err = winshell.OpenPathEx(action.Path, action.RunAsAdmin)
		case "paths.openWith":
			h.mu.Lock()
			detected := slices.Clone(h.detectedTools)
			h.mu.Unlock()
			err = winshell.OpenPathWithTool(action.Path, action.ToolID, action.ToolPath, action.RunAsAdmin, detected)
		case "paths.reveal":
			err = winshell.RevealPath(action.Path)
		case "urls.open":
			err = winshell.OpenURL(action.URL)
		case "icons.refresh":
			key := action.IconKey
			if key == "" && action.URL != "" {
				key = "favicon:" + action.URL
			}
			if key != "" && h.ui != nil {
				h.ui.RefreshIcon(key)
			}
		case "wallpaper.set":
			err = winshell.Wallpaper(action.Path)
		}
		if err != nil {
			event.Error = diagnostic.English(err)
			h.log.Warn("plugin action failed", "plugin", id, "action", action.Type, "error", event.Error)
			if origin != nil && action.ID == "" && origin.dialogs != nil {
				origin.dialogs(origin.ctx, v1.Action{Type: "ui.message", Title: "Could not complete the action", Value: event.Error}, nil)
			} else if origin == nil && (action.Type == "apps.launch" || action.ID == "") {
				h.showError("Could not complete the action", err)
			}
		}
		if action.ID != "" {
			if origin != nil {
				h.resumeTab(origin, event)
			} else {
				h.event(id, event, generation)
			}
		}
	}
}

func (h *Host) reportCall(id, op string, err error) {
	h.log.Warn("plugin request failed", "plugin", id, "operation", op, "error", diagnostic.English(err))
	h.showError("Plugin operation failed", err)
}

func (h *Host) showError(title string, err error) {
	if h.ctx.Err() == nil {
		h.ui.Message(title, diagnostic.English(err))
	}
}

func (h *Host) repeatSearch() {
	h.mu.Lock()
	if h.query != "" {
		h.scheduleSearch(h.query)
	}
	h.mu.Unlock()
}

func (h *Host) search(query string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scheduleSearch(query)
	return h.searchID
}

func (h *Host) scheduleSearch(query string) {
	if h.cancelSearch != nil {
		h.cancelSearch()
	}
	h.query = query
	h.searchID++
	revision := h.searchID
	ctx, cancel := context.WithCancel(h.ctx)
	h.cancelSearch = cancel
	go h.runSearch(ctx, revision, query)
}

type searchResponse struct {
	id         string
	generation uint64
	results    []v1.Result
}

func (h *Host) runSearch(ctx context.Context, revision uint64, query string) {
	term, commandOnly := parseCommandQuery(query)
	if term == "" && !commandOnly {
		h.showSearch(revision, query, nil, "")
		return
	}
	cmdResponses := h.searchPublicCommands(term, commandOnly)
	if commandOnly {
		merged := h.mergeResults(cmdResponses)
		if len(merged) > 0 {
			h.showSearch(revision, query, merged, "")
		} else {
			h.showSearch(revision, query, nil, "empty")
		}
		return
	}
	h.mu.Lock()
	ready := h.indexReady
	var providers []searchResponse
	for id, rec := range h.records {
		if rec.generation != 0 && rec.registration.Search {
			providers = append(providers, searchResponse{id: id, generation: rec.generation})
		}
	}
	h.mu.Unlock()
	all := cmdResponses
	firstShown := false
	started := time.Now()
	if initial := h.mergeResults(all); len(initial) != 0 {
		h.showSearch(revision, query, initial, "")
		firstShown = true
	} else {
		status := "searching"
		if !ready {
			status = "indexing"
		}
		h.showSearch(revision, query, nil, status)
	}
	results := make(chan searchResponse, len(providers))
	for _, response := range providers {
		go func() {
			id := response.id
			snapshot, err := h.db.Snapshot(ctx, id)
			if err == nil {
				payload := h.payload(snapshot)
				payload.Text, payload.Limit = term, 25
				value, err := h.manager.CallGeneration(ctx, id, response.generation, "search", payload)
				if err == nil && h.usable(id, value.Generation) {
					response.results = value.Data.Results
				} else if err != nil && ctx.Err() == nil {
					h.log.Warn("search provider failed", "plugin", id, "error", diagnostic.English(err))
				}
			}
			results <- response
		}()
	}
	for range providers {
		select {
		case <-ctx.Done():
			return
		case response := <-results:
			all = append(all, response)
			merged := h.mergeResults(all)
			if len(merged) != 0 {
				h.showSearch(revision, query, merged, "")
				if !firstShown {
					firstShown = true
					h.log.Debug("first search results", "duration", time.Since(started))
				}
			}
		}
	}
	if len(h.mergeResults(all)) == 0 && ready {
		h.showSearch(revision, query, nil, "empty")
	}
}

func (h *Host) mergeResults(responses []searchResponse) []winui.Result {
	type item struct {
		id         string
		generation uint64
		result     v1.Result
	}
	var sorted []item
	for _, response := range responses {
		for _, result := range response.results {
			sorted = append(sorted, item{response.id, response.generation, result})
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.result.Score != b.result.Score {
			return a.result.Score > b.result.Score
		}
		if a.result.Title != b.result.Title {
			return a.result.Title < b.result.Title
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.result.ID < b.result.ID
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	catalog := h.catalog
	var results []winui.Result
	menuByKind := make(map[string][]winui.ResultMenuAction)
	for _, item := range sorted {
		if len(results) == 50 {
			break
		}
		r := item.result
		rec := h.records[item.id]
		if rec == nil || rec.generation == 0 || rec.generation != item.generation {
			continue
		}
		ref := r.Item
		if ref != nil && ref.Source == "quailcrate.apps" {
			if _, ok := catalog[ref.ID]; !ok {
				ref = nil
			}
		}
		icon := r.Icon
		if icon == "" && slices.Contains(rec.grants, "apps.read") {
			if entry, ok := catalog[r.ID]; ok {
				if entry.Path != "" {
					icon = entry.Path
				} else if entry.AppUserModelID != "" {
					icon = `shell:AppsFolder\` + entry.AppUserModelID
				}
			}
		} else if entry, ok := catalog[icon]; ok && slices.Contains(rec.grants, "apps.read") {
			if entry.Path != "" {
				icon = entry.Path
			} else if entry.AppUserModelID != "" {
				icon = `shell:AppsFolder\` + entry.AppUserModelID
			}
		}
		var actions []winui.ResultMenuAction
		if ref != nil {
			var cached bool
			actions, cached = menuByKind[ref.Kind]
			if !cached {
				actions = h.resultActionsLocked(ref)
				menuByKind[ref.Kind] = actions
			}
		}
		results = append(results, winui.Result{
			ID: r.ID, Generation: item.generation, Item: ref, Actions: actions,
			PluginID: item.id, CommandID: r.Command.ID, Args: r.Command.Args,
			Title: r.Title, Subtitle: r.Subtitle, Icon: icon,
		})
	}
	return results
}

func (h *Host) showSearch(revision uint64, query string, results []winui.Result, status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if revision == h.searchID {
		for i := range results {
			results[i].Revision = revision
		}
		h.visibleResults = results
		h.ui.SetResults(revision, query, results, status)
	}
}

func (h *Host) pluginDataDir(id string) string {
	if h.userDir == "" || id == "" {
		return ""
	}
	profile := filepath.Dir(h.userDir)
	pluginsDataRoot := filepath.Join(profile, "plugins")
	// Avoid colliding with legacy test setups that pass <root>/plugins as the installed userDir.
	if strings.EqualFold(filepath.Clean(h.userDir), filepath.Clean(pluginsDataRoot)) {
		return filepath.Join(profile, "plugin-data", id)
	}
	return filepath.Join(pluginsDataRoot, id)
}

func PrepareDataDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"plugins", "user-plugins", "logs", "cache"} {
		if err := os.MkdirAll(filepath.Join(abs, name), 0700); err != nil {
			return "", err
		}
	}
	return abs, nil
}
