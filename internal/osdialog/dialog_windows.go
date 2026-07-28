//go:build windows

package osdialog

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This is the modern Windows folder picker — the same Explorer-style dialog
// apps like Office use — driven through its COM interface (IFileOpenDialog with
// the "pick folders" option). It is all done with syscalls, so photocull stays
// a single cgo-free binary.
//
// The COM interfaces are modelled as structs whose first field is a pointer to
// their vtable of method addresses. Calling a method is then a SyscallN to the
// right field, passing the interface pointer as the implicit `this` argument.

var (
	ole32 = windows.NewLazySystemDLL("ole32.dll")

	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

const (
	coinitApartment    = 0x2
	clsctxInprocServer = 0x1

	fosPickFolders     = 0x00000020
	fosForceFileSystem = 0x00000040

	sigdnFileSysPath = 0x80058000
)

// iFileDialogVtbl lays out IFileOpenDialog's method table down to GetResult.
// The order matters: it is IUnknown, then IModalWindow, then IFileDialog.
type iFileDialogVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Show           uintptr // IModalWindow
	SetFileTypes   uintptr // IFileDialog from here down
	SetFileTypeIndex,
	GetFileTypeIndex,
	Advise,
	Unadvise,
	SetOptions,
	GetOptions,
	SetDefaultFolder,
	SetFolder,
	GetFolder,
	GetCurrentSelection,
	SetFileName,
	GetFileName,
	SetTitle,
	SetOkButtonLabel,
	SetFileNameLabel,
	GetResult uintptr
}

type iFileDialog struct{ vtbl *iFileDialogVtbl }

type iShellItemVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
	GetAttributes  uintptr
	Compare        uintptr
}

type iShellItem struct{ vtbl *iShellItemVtbl }

// PickFolder shows the modern folder-selection dialog and returns the chosen
// absolute path.
func PickFolder(title string) (string, error) {
	// COM must be used on a single OS thread that has initialised an apartment.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	// S_OK (0) or S_FALSE (1) mean we initialised COM and must balance it.
	if hr == 0 || hr == 1 {
		defer procCoUninitialize.Call()
	}

	var dialog *iFileDialog
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if failed(hr) || dialog == nil {
		// The modern dialog is unavailable (extremely old Windows); let the
		// caller offer a typed path instead.
		return "", ErrUnsupported
	}
	defer syscall.SyscallN(dialog.vtbl.Release, uintptr(unsafe.Pointer(dialog)))

	syscall.SyscallN(dialog.vtbl.SetOptions, uintptr(unsafe.Pointer(dialog)), fosPickFolders|fosForceFileSystem)

	if title != "" {
		if p, err := syscall.UTF16PtrFromString(title); err == nil {
			syscall.SyscallN(dialog.vtbl.SetTitle, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(p)))
		}
	}

	// Show returns a cancel HRESULT when the user closes the dialog.
	if hr, _, _ := syscall.SyscallN(dialog.vtbl.Show, uintptr(unsafe.Pointer(dialog)), 0); failed(hr) {
		return "", ErrCancelled
	}

	var item *iShellItem
	if hr, _, _ := syscall.SyscallN(dialog.vtbl.GetResult, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&item))); failed(hr) || item == nil {
		return "", ErrCancelled
	}
	defer syscall.SyscallN(item.vtbl.Release, uintptr(unsafe.Pointer(item)))

	var pszPath *uint16
	if hr, _, _ := syscall.SyscallN(item.vtbl.GetDisplayName, uintptr(unsafe.Pointer(item)), sigdnFileSysPath, uintptr(unsafe.Pointer(&pszPath))); failed(hr) || pszPath == nil {
		return "", ErrCancelled
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pszPath)))

	return windows.UTF16PtrToString(pszPath), nil
}

// failed reports whether an HRESULT indicates failure (its sign bit is set).
func failed(hr uintptr) bool {
	return int32(hr) < 0
}
