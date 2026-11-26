package winshell

import (
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	v1 "quailcrate/pluginapi/v1"
)

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	shellExecute         = shell32.NewProc("ShellExecuteW")
	shGetFileInfo        = shell32.NewProc("SHGetFileInfoW")
	shGetKnownFolderItem = shell32.NewProc("SHGetKnownFolderItem")
	coInitialize         = ole32.NewProc("CoInitializeEx")
	coUninitialize       = ole32.NewProc("CoUninitialize")
	coCreate             = ole32.NewProc("CoCreateInstance")
	coAllowForeground    = ole32.NewProc("CoAllowSetForegroundWindow")

	iidShellItem    = guid("{43826D1E-E718-42EE-BC55-A1E261C37BFE}")
	iidShellItem2   = guid("{7E9FB0D3-919F-4307-AB2E-9B1860310C93}")
	bhidEnumItems   = guid("{94F60519-2850-4924-AA5A-D15E84868039}")
	iidEnumItems    = guid("{70629033-E363-4A28-A567-0DB78006E6D7}")
	clsidActivation = guid("{45BA127D-10A8-46EA-8AB7-56EA9078943C}")
	iidActivation   = guid("{2E941141-7F97-4756-BA1D-9DECDE894A3D}")
	clsidWallpaper  = guid("{C2CF3110-460E-4FC1-B9D0-8A1C0C9CC4BD}")
	iidWallpaper    = guid("{B92B56A9-8B55-4E14-9A89-0199BBB6F93B}")
	appIDKey        = propertyKey{guid("{9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}"), 5}
)

type propertyKey struct {
	format windows.GUID
	id     uint32
}

type Entry struct {
	App            v1.App
	Path           string
	AppUserModelID string
}

type fileInfo struct {
	icon        uintptr
	iconIndex   int32
	attributes  uint32
	displayName [260]uint16
	typeName    [80]uint16
}

type comObject struct {
	methods *[32]uintptr
}

func guid(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func comError(op string, hr uintptr) error {
	return fmt.Errorf("%s (HRESULT 0x%08X)", op, uint32(hr))
}

func (o *comObject) release() {
	syscall.SyscallN(o.methods[2], uintptr(unsafe.Pointer(o)))
}

func sta() (func(), error) {
	runtime.LockOSThread()
	hr, _, _ := coInitialize.Call(0, 2)
	if failed(hr) {
		runtime.UnlockOSThread()
		return nil, comError("Initialize Windows Shell", hr)
	}
	return func() {
		coUninitialize.Call()
		runtime.UnlockOSThread()
	}, nil
}

func instance(clsid, iid *windows.GUID) (*comObject, error) {
	var out *comObject
	hr, _, _ := coCreate.Call(
		uintptr(unsafe.Pointer(clsid)), 0, 1|4,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)),
	)
	if failed(hr) {
		return nil, comError("Create Windows Shell object", hr)
	}
	return out, nil
}

func Enumerate() ([]Entry, error) {
	finish, err := sta()
	if err != nil {
		return nil, err
	}
	defer finish()
	var entries []Entry
	seen := make(map[string]bool)
	for _, folder := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Programs, windows.FOLDERID_CommonPrograms} {
		dir, err := windows.KnownFolderPath(folder, 0)
		if err != nil {
			continue
		}
		filepath.WalkDir(dir, func(path string, item fs.DirEntry, err error) error {
			if err != nil || item.IsDir() || !strings.EqualFold(filepath.Ext(path), ".lnk") {
				return nil
			}
			key := strings.ToLower(path)
			if seen[key] {
				return nil
			}
			seen[key] = true
			name := displayName(path)
			entries = append(entries, Entry{
				App:  v1.App{ID: fmt.Sprintf("shortcut:%x", sha256.Sum256([]byte(key))), Name: name, Source: "start-menu", Icon: path},
				Path: path,
			})
			return nil
		})
	}
	packaged, packErr := packagedApps()
	entries = append(entries, packaged...)
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].App, entries[j].App
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.ID < b.ID
	})
	return entries, packErr
}

func displayName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	s, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return name
	}
	var data fileInfo
	ok, _, _ := shGetFileInfo.Call(uintptr(unsafe.Pointer(s)), 0, uintptr(unsafe.Pointer(&data)), unsafe.Sizeof(data), 0x200)
	if ok != 0 {
		label := windows.UTF16ToString(data.displayName[:])
		if label != "" {
			return strings.TrimSuffix(label, filepath.Ext(path))
		}
	}
	return name
}

func packagedApps() ([]Entry, error) {
	var folder, items *comObject
	hr, _, _ := shGetKnownFolderItem.Call(
		uintptr(unsafe.Pointer(windows.FOLDERID_AppsFolder)), 0, 0,
		uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&folder)),
	)
	if failed(hr) {
		return nil, comError("Open Windows application catalog", hr)
	}
	defer folder.release()
	hr, _, _ = syscall.SyscallN(folder.methods[3], uintptr(unsafe.Pointer(folder)), 0,
		uintptr(unsafe.Pointer(&bhidEnumItems)), uintptr(unsafe.Pointer(&iidEnumItems)), uintptr(unsafe.Pointer(&items)))
	if failed(hr) {
		return nil, comError("Enumerate Windows application catalog", hr)
	}
	defer items.release()
	var entries []Entry
	seen := make(map[string]bool)
	for {
		var item *comObject
		var fetched uint32
		hr, _, _ = syscall.SyscallN(items.methods[3], uintptr(unsafe.Pointer(items)), 1,
			uintptr(unsafe.Pointer(&item)), uintptr(unsafe.Pointer(&fetched)))
		if failed(hr) {
			return entries, comError("Read Windows application catalog", hr)
		}
		if fetched == 0 || item == nil {
			break
		}
		id := itemAppID(item)
		name := itemName(item)
		item.release()
		if !strings.Contains(id, "!") || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		entries = append(entries, Entry{
			App:            v1.App{ID: "package:" + id, Name: name, Source: "packaged", Icon: `shell:AppsFolder\` + id},
			AppUserModelID: id,
		})
	}
	return entries, nil
}

func itemName(item *comObject) string {
	var name *uint16
	hr, _, _ := syscall.SyscallN(item.methods[5], uintptr(unsafe.Pointer(item)), 0, uintptr(unsafe.Pointer(&name)))
	return readCOMString(hr, name)
}

func itemAppID(item *comObject) string {
	var extended *comObject
	hr, _, _ := syscall.SyscallN(item.methods[0], uintptr(unsafe.Pointer(item)), uintptr(unsafe.Pointer(&iidShellItem2)), uintptr(unsafe.Pointer(&extended)))
	if failed(hr) {
		return ""
	}
	defer extended.release()
	var value *uint16
	hr, _, _ = syscall.SyscallN(extended.methods[17], uintptr(unsafe.Pointer(extended)), uintptr(unsafe.Pointer(&appIDKey)), uintptr(unsafe.Pointer(&value)))
	return readCOMString(hr, value)
}

func readCOMString(hr uintptr, p *uint16) string {
	if p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	if failed(hr) {
		return ""
	}
	return windows.UTF16PtrToString(p)
}

func OpenPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("An absolute file path is required.")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("The file was not found or could not be accessed: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	result, _, _ := shellExecute.Call(0, 0, uintptr(unsafe.Pointer(name)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("Windows could not open the target (error %d).", result)
	}
	return nil
}

// RevealPath opens Windows Explorer with the given path highlighted.
func RevealPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("An absolute file path is required.")
	}
	verb, _ := windows.UTF16PtrFromString("open")
	exe, _ := windows.UTF16PtrFromString("explorer.exe")
	args, err := windows.UTF16PtrFromString(`/select,"` + path + `"`)
	if err != nil {
		return err
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exe)), uintptr(unsafe.Pointer(args)), 0, 1)
	if result <= 32 {
		return fmt.Errorf("Windows Explorer could not reveal the target (error %d).", result)
	}
	return nil
}

func Launch(entry Entry) error {
	if entry.AppUserModelID == "" {
		return OpenPath(entry.Path)
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	manager, err := instance(&clsidActivation, &iidActivation)
	if err != nil {
		return err
	}
	defer manager.release()
	coAllowForeground.Call(uintptr(unsafe.Pointer(manager)), 0)
	id, err := windows.UTF16PtrFromString(entry.AppUserModelID)
	if err != nil {
		return err
	}
	var pid uint32
	hr, _, _ := syscall.SyscallN(manager.methods[3], uintptr(unsafe.Pointer(manager)), uintptr(unsafe.Pointer(id)), 0, 0, uintptr(unsafe.Pointer(&pid)))
	if failed(hr) {
		return comError("Windows could not start the application", hr)
	}
	return nil
}

func Wallpaper(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("An absolute image path is required.")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, format, decodeErr := image.DecodeConfig(file)
	file.Close()
	if decodeErr != nil || (format != "png" && format != "jpeg") {
		return fmt.Errorf("Select a valid JPG or PNG image.")
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	wallpaper, err := instance(&clsidWallpaper, &iidWallpaper)
	if err != nil {
		return err
	}
	defer wallpaper.release()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	hr, _, _ := syscall.SyscallN(wallpaper.methods[3], uintptr(unsafe.Pointer(wallpaper)), 0, uintptr(unsafe.Pointer(name)))
	if failed(hr) {
		return comError("Set the Windows wallpaper", hr)
	}
	return nil
}
