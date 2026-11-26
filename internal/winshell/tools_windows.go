package winshell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	v1 "quailcrate/pluginapi/v1"
)

// DetectedTool describes a locally installed project opener (IDE, editor, terminal, Explorer).
type DetectedTool struct {
	Tool       v1.Tool
	Executable string
}

// DetectTools returns built-in openers plus auto-detected IDEs/editors installed on Windows.
func DetectTools() []DetectedTool {
	tools := []DetectedTool{
		{Tool: v1.Tool{ID: "explorer", Name: "Explorer", Kind: "system"}},
		{Tool: v1.Tool{ID: "terminal", Name: "Terminal", Kind: "terminal"}},
	}

	localApp := os.Getenv("LOCALAPPDATA")
	progFiles := os.Getenv("ProgramFiles")
	progFiles86 := os.Getenv("ProgramFiles(x86)")
	userProfile := os.Getenv("USERPROFILE")

	type probe struct {
		id         string
		name       string
		kind       string
		lookPath   []string
		candidates []string
		globs      []string
	}

	probes := []probe{
		{
			id: "vscode", name: "VS Code", kind: "editor",
			lookPath: []string{"Code.exe"},
			candidates: []string{
				filepath.Join(localApp, "Programs", "Microsoft VS Code", "Code.exe"),
				filepath.Join(progFiles, "Microsoft VS Code", "Code.exe"),
			},
		},
		{
			id: "cursor", name: "Cursor", kind: "editor",
			lookPath: []string{"Cursor.exe"},
			candidates: []string{
				filepath.Join(localApp, "Programs", "cursor", "Cursor.exe"),
				filepath.Join(localApp, "Programs", "Cursor", "Cursor.exe"),
			},
		},
		{
			id: "antigravity", name: "Antigravity", kind: "editor",
			lookPath: []string{"Antigravity.exe"},
			candidates: []string{
				filepath.Join(localApp, "Programs", "Antigravity", "Antigravity.exe"),
				filepath.Join(progFiles, "Antigravity", "Antigravity.exe"),
			},
		},
		{
			id: "windsurf", name: "Windsurf", kind: "editor",
			lookPath: []string{"Windsurf.exe"},
			candidates: []string{
				filepath.Join(localApp, "Programs", "Windsurf", "Windsurf.exe"),
			},
		},
		{
			id: "idea", name: "IntelliJ IDEA", kind: "editor",
			candidates: []string{
				filepath.Join(localApp, "JetBrains", "Toolbox", "scripts", "idea.cmd"),
			},
			globs: []string{
				filepath.Join(progFiles, "JetBrains", "IntelliJ IDEA*", "bin", "idea64.exe"),
				filepath.Join(localApp, "Programs", "IntelliJ IDEA*", "bin", "idea64.exe"),
			},
		},
		{
			id: "goland", name: "GoLand", kind: "editor",
			candidates: []string{
				filepath.Join(localApp, "JetBrains", "Toolbox", "scripts", "goland.cmd"),
			},
			globs: []string{
				filepath.Join(progFiles, "JetBrains", "GoLand*", "bin", "goland64.exe"),
				filepath.Join(localApp, "Programs", "GoLand*", "bin", "goland64.exe"),
			},
		},
		{
			id: "rider", name: "Rider", kind: "editor",
			candidates: []string{
				filepath.Join(localApp, "JetBrains", "Toolbox", "scripts", "rider.cmd"),
			},
			globs: []string{
				filepath.Join(progFiles, "JetBrains", "JetBrains Rider*", "bin", "rider64.exe"),
				filepath.Join(localApp, "Programs", "Rider*", "bin", "rider64.exe"),
			},
		},
		{
			id: "webstorm", name: "WebStorm", kind: "editor",
			candidates: []string{
				filepath.Join(localApp, "JetBrains", "Toolbox", "scripts", "webstorm.cmd"),
			},
			globs: []string{
				filepath.Join(progFiles, "JetBrains", "WebStorm*", "bin", "webstorm64.exe"),
				filepath.Join(localApp, "Programs", "WebStorm*", "bin", "webstorm64.exe"),
			},
		},
		{
			id: "pycharm", name: "PyCharm", kind: "editor",
			candidates: []string{
				filepath.Join(localApp, "JetBrains", "Toolbox", "scripts", "pycharm.cmd"),
			},
			globs: []string{
				filepath.Join(progFiles, "JetBrains", "PyCharm*", "bin", "pycharm64.exe"),
				filepath.Join(localApp, "Programs", "PyCharm*", "bin", "pycharm64.exe"),
			},
		},
		{
			id: "vs", name: "Visual Studio", kind: "editor",
			globs: []string{
				filepath.Join(progFiles, "Microsoft Visual Studio", "*", "*", "Common7", "IDE", "devenv.exe"),
				filepath.Join(progFiles86, "Microsoft Visual Studio", "*", "*", "Common7", "IDE", "devenv.exe"),
			},
		},
		{
			id: "sublime", name: "Sublime Text", kind: "editor",
			candidates: []string{
				filepath.Join(progFiles, "Sublime Text", "sublime_text.exe"),
				filepath.Join(progFiles, "Sublime Text 3", "sublime_text.exe"),
				filepath.Join(userProfile, "scoop", "apps", "sublime-text", "current", "sublime_text.exe"),
			},
		},
	}

	for _, p := range probes {
		if exe := findToolExecutable(p.candidates, p.globs, p.lookPath); exe != "" {
			tools = append(tools, DetectedTool{
				Tool:       v1.Tool{ID: p.id, Name: p.name, Kind: p.kind},
				Executable: exe,
			})
		}
	}
	return tools
}

func findToolExecutable(candidates, globs, lookPath []string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	for _, pattern := range globs {
		if pattern == "" || strings.HasPrefix(pattern, string(filepath.Separator)) {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		// Prefer newest version directory lexicographically.
		for i := len(matches) - 1; i >= 0; i-- {
			if info, statErr := os.Stat(matches[i]); statErr == nil && !info.IsDir() {
				return matches[i]
			}
		}
	}
	for _, name := range lookPath {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// OpenPathWithTool opens a local file or project directory using a detected tool ID or custom executable path.
func OpenPathWithTool(path, toolID, toolPath string, runAsAdmin bool, detected []DetectedTool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("An absolute file path is required.")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("The file was not found or could not be accessed: %w", err)
	}
	switch toolID {
	case "", "system":
		return OpenPathEx(path, runAsAdmin)
	case "explorer":
		if info.IsDir() {
			return OpenPath(path)
		}
		return RevealPath(path)
	case "terminal":
		dir := path
		if !info.IsDir() {
			dir = filepath.Dir(path)
		}
		return openTerminalInDir(dir, runAsAdmin)
	}

	exe := ""
	if toolID != "" {
		for _, item := range detected {
			if item.Tool.ID == toolID {
				exe = item.Executable
				break
			}
		}
		if exe == "" {
			// Re-probe once in case the tool was just installed.
			for _, item := range DetectTools() {
				if item.Tool.ID == toolID {
					exe = item.Executable
					break
				}
			}
		}
		if exe == "" {
			return fmt.Errorf("Tool %q is not installed.", toolID)
		}
	} else {
		if !filepath.IsAbs(toolPath) {
			return fmt.Errorf("An absolute tool path is required.")
		}
		tInfo, statErr := os.Stat(toolPath)
		if statErr != nil || tInfo.IsDir() {
			return fmt.Errorf("The selected tool executable was not found.")
		}
		ext := strings.ToLower(filepath.Ext(toolPath))
		if ext != ".exe" && ext != ".cmd" && ext != ".bat" {
			return fmt.Errorf("Custom opener must be an .exe, .cmd, or .bat file.")
		}
		exe = toolPath
	}

	return launchProgramWithArg(exe, path, runAsAdmin)
}

func openTerminalInDir(dir string, runAsAdmin bool) error {
	if wt, err := exec.LookPath("wt.exe"); err == nil && wt != "" {
		if runAsAdmin {
			return shellExecProgram(wt, fmt.Sprintf(`-d "%s"`, dir), dir, true)
		}
		cmd := exec.Command(wt, "-d", dir)
		cmd.Dir = dir
		return cmd.Start()
	}
	shellExe := "powershell.exe"
	if pwsh, err := exec.LookPath("pwsh.exe"); err == nil && pwsh != "" {
		shellExe = pwsh
	}
	if runAsAdmin {
		return shellExecProgram(shellExe, "-NoExit", dir, true)
	}
	cmd := exec.Command(shellExe, "-NoExit")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010} // CREATE_NEW_CONSOLE
	return cmd.Start()
}

func launchProgramWithArg(exe, target string, runAsAdmin bool) error {
	if runAsAdmin {
		return shellExecProgram(exe, `"`+target+`"`, filepath.Dir(target), true)
	}
	if strings.EqualFold(filepath.Ext(exe), ".cmd") || strings.EqualFold(filepath.Ext(exe), ".bat") {
		cmd := exec.Command("cmd.exe", "/c", exe, target)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd.Start()
	}
	cmd := exec.Command(exe, target)
	return cmd.Start()
}

func shellExecProgram(exe, params, dir string, runAsAdmin bool) error {
	verbStr := "open"
	if runAsAdmin {
		verbStr = "runas"
	}
	verb, err := windows.UTF16PtrFromString(verbStr)
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	var paramPtr, dirPtr uintptr
	if params != "" {
		p, err := windows.UTF16PtrFromString(params)
		if err != nil {
			return err
		}
		paramPtr = uintptr(unsafe.Pointer(p))
	}
	if dir != "" {
		d, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return err
		}
		dirPtr = uintptr(unsafe.Pointer(d))
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), paramPtr, dirPtr, 1)
	if result <= 32 {
		return fmt.Errorf("Windows could not start the tool (error %d).", result)
	}
	return nil
}
