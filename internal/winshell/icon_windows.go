package winshell

import (
	"image"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32Icon = windows.NewLazySystemDLL("user32.dll")
	gdi32Icon  = windows.NewLazySystemDLL("gdi32.dll")

	shCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	extractIconEx               = shell32.NewProc("ExtractIconExW")
	destroyIcon                 = user32Icon.NewProc("DestroyIcon")
	drawIconEx                  = user32Icon.NewProc("DrawIconEx")
	createCompatibleDC          = gdi32Icon.NewProc("CreateCompatibleDC")
	deleteDC                    = gdi32Icon.NewProc("DeleteDC")
	createDIBSection            = gdi32Icon.NewProc("CreateDIBSection")
	selectObject                = gdi32Icon.NewProc("SelectObject")
	deleteObject                = gdi32Icon.NewProc("DeleteObject")
	getObjectW                  = gdi32Icon.NewProc("GetObjectW")
	getDIBits                   = gdi32Icon.NewProc("GetDIBits")

	iidShellItemImageFactory = guid("{BCC18B79-BA16-442F-80C4-8A59C30C463B}")
)

type gdiBitmap struct {
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       uintptr
}

type iconBitmapInfo struct {
	Size          uint32
	Width, Height int32
	Planes, Bits  uint16
	Compression   uint32
	ImageSize     uint32
	Xppm, Yppm    int32
	Colors        uint32
	Important     uint32
}

// ExtractIcon extracts a crisp 32-bit RGBA icon for a Windows file, folder,
// shortcut (.lnk), or shell:AppsFolder\<AUMID> packaged application.
// Returns nil if no icon can be obtained.
func ExtractIcon(target string, size int) *image.RGBA {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if size < 16 {
		size = 48
	}
	if size > 128 {
		size = 128
	}
	if strings.HasPrefix(target, "imgfile:") {
		return LoadCachedIcon(strings.TrimPrefix(target, "imgfile:"))
	}
	finish, err := sta()
	if err == nil {
		defer finish()
		if img := extractShellImageFactory(target, size); img != nil {
			return img
		}
	}
	return extractSHGetFileInfoIcon(target, size)
}

// ExtractIconAtIndex extracts an icon at a specific resource index from an exe/dll/ico file.
func ExtractIconAtIndex(target string, index, size int) *image.RGBA {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if size < 16 {
		size = 64
	}
	if size > 128 {
		size = 128
	}
	ptr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil
	}
	var large uintptr
	count, _, _ := extractIconEx.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(int32(index)),
		uintptr(unsafe.Pointer(&large)),
		0,
		1,
	)
	if count == 0 || large == 0 {
		return ExtractIcon(target, size)
	}
	defer destroyIcon.Call(large)
	return hiconToRGBA(large, size)
}

func extractShellImageFactory(target string, size int) *image.RGBA {
	ptr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil
	}
	var factory *comObject
	hr, _, _ := shCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(ptr)),
		0,
		uintptr(unsafe.Pointer(&iidShellItemImageFactory)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if failed(hr) || factory == nil {
		return nil
	}
	defer factory.release()

	// On Windows amd64, SIZE{cx, cy int32} (8 bytes) is passed by value in one register.
	packedSize := uintptr(uint32(size)) | (uintptr(uint32(size)) << 32)
	const siigbfIconOnly = 0x00000004
	var hbm uintptr
	hr, _, _ = syscall.SyscallN(
		factory.methods[3],
		uintptr(unsafe.Pointer(factory)),
		packedSize,
		siigbfIconOnly,
		uintptr(unsafe.Pointer(&hbm)),
	)
	if failed(hr) || hbm == 0 {
		// Retry without ICONONLY (needed for some UWP/AppsFolder tiles).
		hr, _, _ = syscall.SyscallN(
			factory.methods[3],
			uintptr(unsafe.Pointer(factory)),
			packedSize,
			0,
			uintptr(unsafe.Pointer(&hbm)),
		)
	}
	if failed(hr) || hbm == 0 {
		return nil
	}
	defer deleteObject.Call(hbm)
	return hbitmapToRGBA(hbm)
}

func hbitmapToRGBA(hbm uintptr) *image.RGBA {
	var bm gdiBitmap
	if ret, _, _ := getObjectW.Call(hbm, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ret == 0 {
		return nil
	}
	w, h := int(bm.Width), int(bm.Height)
	if h < 0 {
		h = -h
	}
	if w <= 0 || h <= 0 || w > 512 || h > 512 {
		return nil
	}
	dc, _, _ := createCompatibleDC.Call(0)
	if dc == 0 {
		return nil
	}
	defer deleteDC.Call(dc)

	buf := make([]byte, w*h*4)
	bi := iconBitmapInfo{
		Size:   40,
		Width:  int32(w),
		Height: -int32(h),
		Planes: 1,
		Bits:   32,
	}
	lines, _, _ := getDIBits.Call(dc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0)
	if lines == 0 {
		return nil
	}
	return bgraBufferToRGBA(buf, w, h)
}

func extractSHGetFileInfoIcon(target string, size int) *image.RGBA {
	if strings.HasPrefix(strings.ToLower(target), "shell:") {
		return nil
	}
	const (
		shgfiIcon              = 0x000000100
		shgfiUseFileAttributes = 0x000000010
		fileAttrNormal         = 0x00000080
		fileAttrDirectory      = 0x00000010
	)
	ptr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil
	}
	var info fileInfo
	ok, _, _ := shGetFileInfo.Call(uintptr(unsafe.Pointer(ptr)), 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), shgfiIcon)
	if ok == 0 || info.icon == 0 {
		attr := uintptr(fileAttrNormal)
		if filepath.Ext(target) == "" {
			attr = fileAttrDirectory
		}
		ok, _, _ = shGetFileInfo.Call(uintptr(unsafe.Pointer(ptr)), attr, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), shgfiIcon|shgfiUseFileAttributes)
		if ok == 0 || info.icon == 0 {
			return nil
		}
	}
	defer destroyIcon.Call(info.icon)
	return hiconToRGBA(info.icon, size)
}

func hiconToRGBA(hicon uintptr, size int) *image.RGBA {
	dc, _, _ := createCompatibleDC.Call(0)
	if dc == 0 {
		return nil
	}
	defer deleteDC.Call(dc)

	bi := iconBitmapInfo{
		Size:   40,
		Width:  int32(size),
		Height: -int32(size),
		Planes: 1,
		Bits:   32,
	}
	var bits uintptr
	bmp, _, _ := createDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		return nil
	}
	defer deleteObject.Call(bmp)
	prev, _, _ := selectObject.Call(dc, bmp)
	defer selectObject.Call(dc, prev)

	byteLen := size * size * 4
	raw := unsafe.Slice((*byte)(unsafe.Pointer(bits)), byteLen)
	clear(raw)

	const diNormal = 0x0003
	if ok, _, _ := drawIconEx.Call(dc, 0, 0, hicon, uintptr(size), uintptr(size), 0, 0, diNormal); ok == 0 {
		return nil
	}
	copied := make([]byte, byteLen)
	copy(copied, raw)
	return bgraBufferToRGBA(copied, size, size)
}

// bgraBufferToRGBA converts a 32-bit BGRA DIB buffer into Go's premultiplied RGBA.
func bgraBufferToRGBA(buf []byte, w, h int) *image.RGBA {
	hasAlpha := false
	hasColor := false
	for i := 0; i < len(buf); i += 4 {
		if buf[i+3] != 0 {
			hasAlpha = true
		}
		if buf[i] != 0 || buf[i+1] != 0 || buf[i+2] != 0 {
			hasColor = true
		}
	}
	if !hasAlpha && !hasColor {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(buf); i += 4 {
		b, g, r, a := buf[i], buf[i+1], buf[i+2], buf[i+3]
		if !hasAlpha {
			if r != 0 || g != 0 || b != 0 {
				a = 255
			}
		} else {
			// Ensure channels are premultiplied for Go image.RGBA & Gio paint.NewImageOp.
			if r > a || g > a || b > a {
				r = uint8(uint16(r) * uint16(a) / 255)
				g = uint8(uint16(g) * uint16(a) / 255)
				b = uint8(uint16(b) * uint16(a) / 255)
			}
		}
		img.Pix[i] = r
		img.Pix[i+1] = g
		img.Pix[i+2] = b
		img.Pix[i+3] = a
	}
	return img
}
