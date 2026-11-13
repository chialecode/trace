//go:build !windows

package shell

func popUpTrayMenu([]trayMenuEntry) {}

// nativeTrayMenu reports whether popUpTrayMenu replaces MyGo's tray menu;
// elsewhere the menu stays MyGo's.
const nativeTrayMenu = false
