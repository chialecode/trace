//go:build windows

package shell

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The tray menu is a plain Win32 popup opened at the pointer, as most
// Windows apps' tray menus are: MyGo's own tray menu opens Windows 11
// style, centered on the icon 12 DIPs above the taskbar and in a larger
// font, which looked detached from the icon (2026-11-15 feedback). A plain
// menu takes the system's menu font and Windows 11's rounded look.
var (
	trayUser32           = windows.NewLazySystemDLL("user32.dll")
	procCreatePopupMenu  = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = trayUser32.NewProc("AppendMenuW")
	procSetMenuDefault   = trayUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenuEx = trayUser32.NewProc("TrackPopupMenuEx")
	procDestroyMenu      = trayUser32.NewProc("DestroyMenu")
	procGetCursorPos     = trayUser32.NewProc("GetCursorPos")
	procSetForeground    = trayUser32.NewProc("SetForegroundWindow")
	procPostMessageW     = trayUser32.NewProc("PostMessageW")
	procCreateWindowExW  = trayUser32.NewProc("CreateWindowExW")
)

const (
	mfString       = 0x0000
	mfSeparator    = 0x0800
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	wsPopup        = 0x80000000
	wmNull         = 0x0000
)

// trayMenuOwner is a hidden window that owns the popup, made on the UI
// thread the first time the menu opens and kept for the process.
var trayMenuOwner uintptr

// buildPopupTrayMenu creates the native Win32 popup menu for entries, setting
// the first item (Open) as the default menu item and inserting a separator
// before the final item (Quit) when there are two or more entries.
func buildPopupTrayMenu(entries []trayMenuEntry) uintptr {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return 0
	}
	appendedFirst := false
	for i, e := range entries {
		label, err := windows.UTF16PtrFromString(e.label)
		if err != nil {
			continue
		}
		if len(entries) >= 2 && i == len(entries)-1 && appendedFirst {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
		}
		procAppendMenuW.Call(menu, mfString, uintptr(i+1), uintptr(unsafe.Pointer(label)))
		if i == 0 {
			appendedFirst = true
		}
	}
	if appendedFirst {
		procSetMenuDefault.Call(menu, 1, 0)
	}
	return menu
}

// popUpTrayMenu shows entries at the pointer and runs the chosen one. It
// must run on the UI thread, as tray events do.
func popUpTrayMenu(entries []trayMenuEntry) {
	if trayMenuOwner == 0 {
		cls, _ := windows.UTF16PtrFromString("STATIC")
		h, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, wsPopup, 0, 0, 0, 0, 0, 0, 0, 0)
		if h == 0 {
			return
		}
		trayMenuOwner = h
	}
	menu := buildPopupTrayMenu(entries)
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// The owner must be foreground for the menu to close on a click
	// elsewhere, and the posted message lets a second right click reopen it.
	procSetForeground.Call(trayMenuOwner)
	cmd, _, _ := procTrackPopupMenuEx.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), trayMenuOwner, 0)
	procPostMessageW.Call(trayMenuOwner, wmNull, 0, 0)
	if i := int(cmd) - 1; i >= 0 && i < len(entries) && entries[i].run != nil {
		entries[i].run()
	}
}

// nativeTrayMenu reports whether popUpTrayMenu replaces MyGo's tray menu.
const nativeTrayMenu = true
