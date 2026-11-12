; TRACE installer hooks (MyGo windows.installerInclude). Each channel's file (stable.nsh, dev.nsh,
; ptest.nsh, chosen by tools/internal/build.ChannelConfig) defines TRACE_SHORTCUT, the shortcuts'
; file name (the channel's windows.displayName), and the development channel TRACE_DESKTOP_ON_UPDATE.
;
; MyGo always makes the Start menu shortcut and makes the desktop one from its finish page's box.
; With these hooks:
;   silent first install: both shortcuts;
;   silent over-install (tools/package's install-dev, install.ps1): the shortcuts there stay and no
;     new one is made, except that the development channel makes the desktop shortcut again, a sign
;     that a new build was installed;
;   interactive install: the finish page's box chooses the desktop shortcut, a question during the
;     install the Start menu one (MyGo's hooks cannot add a wizard page).
; Release updates replace the app's files without the installer and change no shortcut.

!include LogicLib.nsh

Var TraceFirstInstall
Var TraceHadStartMenu

!macro customInit
  ; InstallDirRegKey has set $INSTDIR to an installed version's folder, if there is one.
  StrCpy $TraceFirstInstall "1"
  ${If} ${FileExists} "$INSTDIR\Uninstall.exe"
    StrCpy $TraceFirstInstall "0"
  ${EndIf}
  StrCpy $TraceHadStartMenu "0"
  ${If} ${FileExists} "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
    StrCpy $TraceHadStartMenu "1"
  ${EndIf}
!macroend

; At the end of the install, after MyGo made the Start menu shortcut.
!macro customInstall
  ${If} ${Silent}
    ${If} $TraceFirstInstall != "1"
    ${AndIf} $TraceHadStartMenu != "1"
      Delete "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
    ${EndIf}
    ${If} $TraceFirstInstall == "1"
      Call CreateDesktopShortcut
    ${EndIf}
    !ifdef TRACE_DESKTOP_ON_UPDATE
      ${If} $TraceFirstInstall != "1"
        Call CreateDesktopShortcut
      ${EndIf}
    !endif
  ${Else}
    ; Yes is the default on a first install and when the shortcut was there.
    ${If} $TraceFirstInstall == "1"
    ${OrIf} $TraceHadStartMenu == "1"
      ${If} ${Cmd} `MessageBox MB_YESNO|MB_ICONQUESTION "是否在开始菜单创建 ${TRACE_SHORTCUT} 的快捷方式？" IDNO`
        Delete "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
      ${EndIf}
    ${Else}
      ${If} ${Cmd} `MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "是否在开始菜单创建 ${TRACE_SHORTCUT} 的快捷方式？" IDNO`
        Delete "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
      ${EndIf}
    ${EndIf}
  ${EndIf}
!macroend
