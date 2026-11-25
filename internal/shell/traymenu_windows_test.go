//go:build windows

package shell

import (
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
)

var (
	procGetMenuItemCount   = trayUser32.NewProc("GetMenuItemCount")
	procGetMenuState       = trayUser32.NewProc("GetMenuState")
	procGetMenuItemID      = trayUser32.NewProc("GetMenuItemID")
	procGetMenuDefaultItem = trayUser32.NewProc("GetMenuDefaultItem")
)

const mfByPosition = 0x0400

func TestBuildPopupTrayMenuSeparatorAndDefaultItem(t *testing.T) {
	s := &Shell{loc: appdirs.Locations{DisplayName: "TRACE Dev"}}
	menu := buildPopupTrayMenu(s.trayMenuEntries())
	if menu == 0 {
		t.Fatal("buildPopupTrayMenu returned 0")
	}
	defer procDestroyMenu.Call(menu)

	count, _, _ := procGetMenuItemCount.Call(menu)
	if count != 3 {
		t.Fatalf("GetMenuItemCount = %d, want 3 (Open, separator, Quit)", count)
	}

	openID, _, _ := procGetMenuItemID.Call(menu, 0)
	if openID != 1 {
		t.Fatalf("item 0 ID = %d, want 1", openID)
	}
	sepState, _, _ := procGetMenuState.Call(menu, 1, mfByPosition)
	if sepState&mfSeparator == 0 {
		t.Fatalf("item 1 state = 0x%x, expected MF_SEPARATOR (0x%x)", sepState, mfSeparator)
	}
	quitID, _, _ := procGetMenuItemID.Call(menu, 2)
	if quitID != 2 {
		t.Fatalf("item 2 ID = %d, want 2", quitID)
	}
	defID, _, _ := procGetMenuDefaultItem.Call(menu, 0, 0)
	if defID != 1 {
		t.Fatalf("GetMenuDefaultItem = %d, want 1 (Open)", defID)
	}

	single := buildPopupTrayMenu([]trayMenuEntry{{label: "打开 TRACE", run: func() {}}})
	if single == 0 {
		t.Fatal("buildPopupTrayMenu(single) returned 0")
	}
	defer procDestroyMenu.Call(single)
	singleCount, _, _ := procGetMenuItemCount.Call(single)
	if singleCount != 1 {
		t.Fatalf("single-entry GetMenuItemCount = %d, want 1", singleCount)
	}
	singleDef, _, _ := procGetMenuDefaultItem.Call(single, 0, 0)
	if singleDef != 1 {
		t.Fatalf("single-entry GetMenuDefaultItem = %d, want 1", singleDef)
	}
}
