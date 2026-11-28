package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"quailcrate/internal/plugininstall"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/update"
	"quailcrate/internal/winshell"
)

type pluginInstallStatus struct {
	busy, uninstalling, failed bool
	english, chinese           string
}

var (
	errBundledPlugin  = errors.New("cannot replace or remove a bundled plugin")
	errPluginPath     = errors.New("plugin directory is not a regular direct child of its root")
	errPluginClash    = errors.New("conflicting plugin directories")
	errPluginMissing  = errors.New("plugin package is no longer available")
	errPluginChanged  = errors.New("plugin package changed during confirmation")
	errPluginDeclined = errors.New("plugin change declined")
	errPluginStop     = errors.New("plugin is still stopping")
	errPluginCleanup  = errors.New("committed plugin change left old files in the backup directory")
)

func (h *Host) beginPluginChange(uninstalling bool) bool {
	h.mu.Lock()
	if h.pluginInstall.busy || h.ctx.Err() != nil {
		h.mu.Unlock()
		return false
	}
	h.pluginInstall = pluginInstallStatus{busy: true, uninstalling: uninstalling}
	h.mu.Unlock()
	h.publish()
	return true
}

func (h *Host) publishPluginChange(status pluginInstallStatus) {
	h.mu.Lock()
	h.pluginInstall = status
	h.mu.Unlock()
	if h.ctx.Err() == nil {
		h.publish()
	}
}

func (h *Host) pluginChangeResult(err error, english, chinese string) {
	status := pluginInstallStatus{english: english, chinese: chinese}
	if errors.Is(err, errPluginDeclined) {
		status = pluginInstallStatus{}
	} else if err != nil {
		h.log.Warn("manage plugin package", "error", err)
		status = pluginInstallStatus{failed: true, english: pluginChangeError(err, "en-US"), chinese: pluginChangeError(err, "zh-CN")}
	}
	h.publishPluginChange(status)
}

func (h *Host) currentLocale() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.locale
}

func (h *Host) PromptInstallFromGitHub() {
	if !h.beginPluginChange(false) {
		return
	}
	h.ui.PromptTextFor(h.ctx, 0, text(h.currentLocale(), "Enter a GitHub repository or release link", "输入 GitHub 仓库或 Release 链接"), "", func(raw string, ok bool) {
		if !ok {
			h.publishPluginChange(pluginInstallStatus{})
			return
		}
		go h.installPluginFromGitHub(raw)
	})
}

func (h *Host) installPluginFromGitHub(raw string) {
	h.publishPluginChange(pluginInstallStatus{busy: true,
		english: "Downloading and verifying the plugin from GitHub…",
		chinese: "正在从 GitHub 下载并校验插件…",
	})
	installer := plugininstall.Installer{CacheDir: h.cacheDir, HostVersion: h.releaseVersion, Logger: h.log}
	staged, err := installer.Stage(h.ctx, raw)
	if err != nil {
		h.pluginChangeResult(err, "", "")
		return
	}
	manifest := staged.Package.Manifest
	err = h.commitPluginInstall(staged)
	staged.Cleanup()
	h.pluginChangeResult(err,
		fmt.Sprintf("Installed %s v%s. Manage it in the plugin list.", manifest.DisplayName("en-US"), manifest.Version),
		fmt.Sprintf("已安装 %s v%s，可在插件列表中管理。", manifest.DisplayName("zh-CN"), manifest.Version))
}

func (h *Host) commitPluginInstall(staged plugininstall.StagedPlugin) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	m := staged.Package.Manifest
	old, err := h.installedPackage(m.ID)
	if err != nil {
		return err
	}
	target, err := pluginChild(h.userDir, m.ID)
	if err != nil {
		return err
	}
	var legacy string
	if old.Dir != "" {
		path, err := h.installedPackagePath(old.Dir)
		if err != nil {
			return err
		}
		if !strings.EqualFold(path, target) {
			legacy = path
		}
	}
	if old.Dir == "" || legacy != "" {
		if _, err := os.Lstat(target); err == nil {
			return errPluginClash
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if old.Dir != "" && update.Newer(old.Manifest.Version, m.Version) {
		locale := h.currentLocale()
		name := m.DisplayName(locale)
		if !h.confirmPluginChange(text(locale, "Install an older plugin version?", "安装较旧的插件版本？"),
			fmt.Sprintf(text(locale, "Replace %s v%s with v%s?", "将 %s 从 v%s 降级到 v%s？"), name, old.Manifest.Version, m.Version)) {
			return errPluginDeclined
		}
		if err := h.checkConfirmedPackage(old); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(h.userDir, 0700); err != nil {
		return err
	}
	if err := h.stopPluginForChange(m.ID); err != nil {
		return err
	}
	defer h.scanLocked()
	meta := store.InstalledPlugin{
		ID: m.ID, SourceURL: staged.Source.URL, RepoOwner: staged.Source.Owner, RepoName: staged.Source.Repo,
		RefOrTag: staged.RefOrTag, AssetName: staged.AssetName, InstalledVersion: m.Version, InstalledAt: time.Now().Unix(),
	}
	save := func() error {
		if old.Dir == "" {
			return h.db.SetNewInstalledPlugin(h.ctx, meta)
		}
		return h.db.SetInstalledPlugin(h.ctx, meta)
	}
	install := func() error { return plugininstall.CommitSwap(target, staged.Package.Dir, save) }
	if legacy != "" {
		return h.retirePluginDirs([]string{legacy}, install)
	}
	return install()
}

func (h *Host) UninstallPlugin(id string) {
	if !h.beginPluginChange(true) {
		return
	}
	pkg, err := h.uninstallPlugin(id)
	m := pkg.Manifest
	h.pluginChangeResult(err, "Uninstalled "+m.DisplayName("en-US")+".", "已卸载 "+m.DisplayName("zh-CN")+"。")
}

func (h *Host) uninstallPlugin(id string) (plugins.Package, error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if err := h.ctx.Err(); err != nil {
		return plugins.Package{}, err
	}
	pkg, err := h.installedPackage(id)
	if err != nil {
		return pkg, err
	}
	if pkg.Dir == "" {
		return pkg, errPluginMissing
	}
	locale := h.currentLocale()
	name := pkg.Manifest.DisplayName(locale)
	if !h.confirmPluginChange(text(locale, "Uninstall "+name+"?", "卸载「"+name+"」？"), text(locale,
		"This removes the plugin files, local data and state, permission grants, and widget layouts.",
		"此操作会删除插件程序文件、本地数据与状态、权限授权和桌面组件布局。")) {
		return pkg, errPluginDeclined
	}
	h.publishPluginChange(pluginInstallStatus{busy: true, uninstalling: true, english: "Uninstalling the plugin…", chinese: "正在卸载插件…"})
	if err := h.stopPluginForChange(id); err != nil {
		return pkg, err
	}
	defer h.scanLocked()
	if err := h.checkConfirmedPackage(pkg); err != nil {
		return pkg, err
	}
	path, err := h.installedPackagePath(pkg.Dir)
	if err != nil {
		return pkg, err
	}
	paths := []string{path, h.pluginDataDir(id), h.files.PrivateDataDir(id), h.network.PluginSessionDir(id)}
	for _, dataPath := range paths[1:] {
		if _, err := pluginChild(filepath.Dir(dataPath), filepath.Base(dataPath)); err != nil {
			return pkg, err
		}
	}
	h.network.ForgetPluginSessions(id)
	return pkg, h.retirePluginDirs(paths, func() error { return h.db.PurgePlugin(h.ctx, id) })
}

func (h *Host) confirmPluginChange(title, body string) bool {
	answer := make(chan bool, 1)
	h.confirm(title, body, func(ok bool) { answer <- ok })
	select {
	case confirmed := <-answer:
		return confirmed && h.ctx.Err() == nil
	case <-h.ctx.Done():
		return false
	}
}

func (h *Host) stopPluginForChange(id string) error {
	h.mu.Lock()
	rec := h.records[id]
	active := rec != nil && rec.generation != 0
	h.mu.Unlock()
	var err error
	if active {
		err = h.disable(rec)
	} else if h.manager.Stopping(id) {
		ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
		defer cancel()
		err = h.manager.Disable(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errPluginStop, err)
	}
	return h.ctx.Err()
}

func (h *Host) checkConfirmedPackage(pkg plugins.Package) error {
	found, err := h.installedPackage(pkg.Manifest.ID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(found.Dir, pkg.Dir) || found.Manifest.Version != pkg.Manifest.Version {
		return errPluginChanged
	}
	return nil
}

func manageableUserPackage(pkg plugins.Package) bool {
	id := pkg.Manifest.ID
	if pkg.Bundled || id == "quailcrate" || strings.HasPrefix(id, "quailcrate.") || !filepath.IsLocal(id) {
		return false
	}
	return pkg.Err == nil || plugins.ValidateManifest(pkg.Manifest) == nil
}

// Re-read package directories under the lifecycle lock so filesystem changes
// since the last settings scan cannot redirect an install or uninstall.
func (h *Host) installedPackage(id string) (plugins.Package, error) {
	if id == "" {
		return plugins.Package{}, errPluginMissing
	}
	var existing plugins.Package
	matches := 0
	for _, pkg := range h.pluginCatalog.Discover(h.bundledDir, h.userDir) {
		if pkg.Manifest.ID != id {
			continue
		}
		if pkg.Bundled {
			return plugins.Package{}, errBundledPlugin
		}
		matches++
		if manageableUserPackage(pkg) {
			existing = pkg
		}
	}
	if matches > 1 {
		return plugins.Package{}, errPluginClash
	}
	return existing, nil
}

func (h *Host) installedPackagePath(absolutePath string) (string, error) {
	root, err := filepath.Abs(h.userDir)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	name, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return "", errPluginPath
	}
	return pluginChild(h.userDir, name)
}

func pluginChild(root, name string) (string, error) {
	if root == "" || !filepath.IsLocal(name) || name == "." || filepath.Base(name) != name {
		return "", errPluginPath
	}
	path := filepath.Join(root, name)
	for _, dir := range []string{root, path} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		attrs, _ := info.Sys().(*syscall.Win32FileAttributeData)
		if !info.IsDir() || attrs != nil && attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return "", errPluginPath
		}
	}
	return path, nil
}

// Moving the directories out of discovery first lets us restore both of them
// if either move or the database write fails.
func (h *Host) retirePluginDirs(paths []string, commit func() error) (result error) {
	if err := os.MkdirAll(h.cacheDir, 0700); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(h.cacheDir, "plugin-backup-")
	if err != nil {
		return err
	}
	moved := make([]bool, len(paths))
	defer func() {
		restored := true
		if result != nil {
			for i := len(paths) - 1; i >= 0; i-- {
				if moved[i] {
					if err := os.Rename(filepath.Join(backup, strconv.Itoa(i)), paths[i]); err != nil {
						result = errors.Join(result, fmt.Errorf("restore plugin directory from %s: %w", backup, err))
						restored = false
					}
				}
			}
		}
		if restored {
			if err := os.RemoveAll(backup); err != nil {
				cleanupErr := fmt.Errorf("%w: %w", errPluginCleanup, err)
				if result == nil {
					result = cleanupErr
				} else {
					h.log.Warn("remove plugin directory backup", "path", backup, "error", err)
				}
			}
		}
	}()
	for i, path := range paths {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(path, filepath.Join(backup, strconv.Itoa(i))); err != nil {
			return err
		}
		moved[i] = true
	}
	return commit()
}

func (h *Host) openPluginHomepage(id string) {
	h.mu.Lock()
	var homepage string
	if rec := h.records[id]; rec != nil {
		homepage = rec.info.HomepageURL
	}
	locale := h.locale
	h.mu.Unlock()
	if homepage != "" {
		if err := winshell.OpenURL(homepage); err != nil {
			h.showError(text(locale, "Could not open the plugin homepage", "无法打开插件主页"), err)
		}
	}
}

func pluginChangeError(err error, locale string) string {
	var installErr *plugininstall.Error
	if errors.As(err, &installErr) {
		switch installErr.Code {
		case plugininstall.ErrorSource:
			return text(locale, "Enter a valid GitHub repository, release, ZIP asset, or tree link.", "请输入有效的 GitHub 仓库、Release、ZIP 附件或分支目录链接。")
		case plugininstall.ErrorNetwork:
			return text(locale, "Could not connect to GitHub. Check your connection and try again.", "无法连接 GitHub。请检查网络连接后重试。")
		case plugininstall.ErrorRateLimit:
			return text(locale, "GitHub’s API limit was reached. Try again later or paste the direct Release ZIP download link.", "GitHub API 已达限流。请稍后重试，或粘贴 Release ZIP 附件的下载直链。")
		case plugininstall.ErrorRelease:
			return text(locale, "Could not load the requested GitHub Release. Check that the release exists and is public.", "无法读取指定的 GitHub Release。请确认该版本存在且公开可访问。")
		case plugininstall.ErrorAssets:
			return text(locale, "This GitHub Release has no installable ZIP attachment.", "此 GitHub Release 没有可安装的 ZIP 附件。")
		case plugininstall.ErrorAmbiguousAssets:
			return fmt.Sprintf(text(locale, "Paste the direct download link for one of these Release ZIPs: %s", "请粘贴以下某个 Release ZIP 附件的下载直链：%s"), installErr.Detail)
		case plugininstall.ErrorDownload:
			return text(locale, "The GitHub ZIP could not be downloaded or exceeds the 128 MiB limit.", "无法下载 GitHub ZIP，或压缩包超过 128 MiB 上限。")
		case plugininstall.ErrorChecksum:
			return text(locale, "The Release ZIP failed SHA-256 verification.", "Release ZIP 未通过 SHA-256 校验。")
		case plugininstall.ErrorArchive:
			return text(locale, "The ZIP is invalid, contains unsafe entries, or exceeds the extraction limits.", "ZIP 无效、包含不安全的条目，或超过解压配额。")
		case plugininstall.ErrorManifestMissing:
			return text(locale, "The ZIP or selected subdirectory has no manifest.json at its package root.", "ZIP 或指定子目录的插件包根目录缺少 manifest.json。")
		case plugininstall.ErrorManifestAmbiguous:
			return text(locale, "The ZIP contains multiple plugin manifests. Use a tree link to the intended plugin subdirectory.", "ZIP 内有多个插件清单。请使用指向目标插件子目录的链接。")
		case plugininstall.ErrorPackage:
			return text(locale, "The plugin manifest, entry file, or package resources are invalid.", "插件清单、入口文件或包内资源无效。")
		case plugininstall.ErrorNativeSource:
			return text(locale, "This native DLL plugin requires the author’s prebuilt GitHub Release ZIP.", "该插件为原生 DLL 插件，请从作者发布的 GitHub Release 预编译包安装。")
		case plugininstall.ErrorWebSource:
			return text(locale, "This plugin includes web assets. Install the author’s packaged GitHub Release ZIP.", "该插件包含 Web 资源，请从作者发布的 GitHub Release 打包附件安装。")
		case plugininstall.ErrorReservedID:
			return text(locale, "This plugin ID is reserved for bundled plugins.", "该插件 ID 保留给内置插件。")
		case plugininstall.ErrorHostVersion:
			return fmt.Sprintf(text(locale, "This plugin requires QuailCrate v%s or later. Update QuailCrate before installing it.", "该插件要求鹌鹑工具箱 v%s 或更高版本。请升级后再安装。"), installErr.Detail)
		}
	}
	switch {
	case errors.Is(err, errBundledPlugin):
		return text(locale, "Bundled plugins cannot be replaced or uninstalled here.", "无法在此覆盖或卸载内置插件。")
	case errors.Is(err, errPluginPath):
		return text(locale, "The plugin folder or its data folder has an unsupported path.", "插件目录或数据目录的路径不受支持。")
	case errors.Is(err, errPluginClash):
		return text(locale, "Multiple folders use this plugin ID, or another file occupies its install path.", "多个目录使用了同一个插件 ID，或安装路径已被其他文件占用。")
	case errors.Is(err, errPluginMissing):
		return text(locale, "The plugin is no longer available. Rescan the plugin list and try again.", "找不到该插件。请重新扫描插件列表后重试。")
	case errors.Is(err, errPluginChanged):
		return text(locale, "The plugin changed during confirmation. Rescan the plugin list and try again.", "插件在确认期间发生了变化。请重新扫描插件列表后重试。")
	case errors.Is(err, errPluginStop):
		return text(locale, "The plugin is still stopping. Try again in a moment.", "插件仍在停用，请稍后重试。")
	case errors.Is(err, errPluginCleanup):
		return text(locale, "The plugin change was saved. Some old files remain in the cache.", "插件变更已保存。缓存目录中仍有部分旧文件。")
	default:
		return text(locale, "Could not save the plugin change. Check that QuailCrate can write to its data folder.", "无法保存插件变更。请检查鹌鹑工具箱是否能写入数据目录。")
	}
}
