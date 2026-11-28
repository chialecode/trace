; TRACE installer hooks (MyGo windows.installerInclude). Each channel's file (stable.nsh, dev.nsh,
; test.nsh, chosen by tools/internal/build.ChannelConfig) defines TRACE_SHORTCUT, the shortcuts'
; file name (the channel's windows.displayName), TRACE_AUTOSTART_VALUE, the per-channel HKCU Run
; value name cleaned up on uninstall, and the development channel TRACE_DESKTOP_ON_UPDATE.
;
; tools/internal/build.MygoBuild patches MyGo's generated installer.nsi (via tools/internal/nsiswrap)
; so the Start menu shortcut step delegates to customStartMenuShortcut instead of running
; CreateShortCut unconditionally, and strips cross-channel trace.lnk cleanup on dev/test channels.
;
; With these hooks:
;   silent first install: both shortcuts (desktop skipped when /NODESKTOP is passed);
;   silent over-install (tools/package's install-dev, install.ps1): Start menu shortcut is never
;     created, overwritten, or deleted (absent stays absent, existing keeps its original file and
;     timestamp); desktop shortcut is left alone except that the development channel creates it
;     again unless /NODESKTOP is passed, a sign that a new build was installed;
;   interactive install: asks before creating the Start menu shortcut (default Yes on first install
;     or when already present, default No on over-install when previously removed); the finish
;     page's checkbox chooses the desktop shortcut.
; Release updates replace the app's files without the installer and change no shortcut.
; Full uninstall removes both shortcuts, this channel's per-user Windows login autostart value
; (HKCU\Software\Microsoft\Windows\CurrentVersion\Run\${TRACE_AUTOSTART_VALUE}), and the install
; folder, preserving user data and never touching other channels.
;
; /RELAUNCH (silent installs only) starts TRACE once the install is done, as the other desktop
; apps of this author do; TRACE's own "quit for update" record decides window or tray.
; tools/package starts TRACE Dev itself instead, outside the command's job object.

!include LogicLib.nsh
!include FileFunc.nsh

!ifndef TRACE_RUN_REGKEY
  !define TRACE_RUN_REGKEY "Software\Microsoft\Windows\CurrentVersion\Run"
!endif

Var TraceFirstInstall
Var TraceHadStartMenu

!macro customInit
  ; InstallDirRegKey (or /D=) has set $INSTDIR to an installed version's folder, if there is one.
  StrCpy $TraceFirstInstall "1"
  ${If} ${FileExists} "$INSTDIR\Uninstall.exe"
    StrCpy $TraceFirstInstall "0"
  ${EndIf}
  StrCpy $TraceHadStartMenu "0"
  ${If} ${FileExists} "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
    StrCpy $TraceHadStartMenu "1"
  ${EndIf}
!macroend

; Replaces MyGo's unconditional CreateShortCut "$SMPROGRAMS\..." via tools/internal/nsiswrap.
!macro customStartMenuShortcut
  ${If} ${Silent}
    ${If} $TraceFirstInstall == "1"
      SetOutPath "$INSTDIR"
      CreateShortCut "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk" "$INSTDIR\trace.exe"
    ${EndIf}
  ${Else}
    ; Yes is the default on a first install and when the shortcut was already there.
    ${If} $TraceFirstInstall == "1"
    ${OrIf} $TraceHadStartMenu == "1"
      ${If} ${Cmd} `MessageBox MB_YESNO|MB_ICONQUESTION "是否在开始菜单创建 ${TRACE_SHORTCUT} 的快捷方式？" IDYES`
        SetOutPath "$INSTDIR"
        CreateShortCut "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk" "$INSTDIR\trace.exe"
      ${Else}
        ${If} $TraceHadStartMenu == "1"
          Delete "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk"
        ${EndIf}
      ${EndIf}
    ${Else}
      ${If} ${Cmd} `MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "是否在开始菜单创建 ${TRACE_SHORTCUT} 的快捷方式？" IDYES`
        SetOutPath "$INSTDIR"
        CreateShortCut "$SMPROGRAMS\${TRACE_SHORTCUT}.lnk" "$INSTDIR\trace.exe"
      ${EndIf}
    ${EndIf}
  ${EndIf}
!macroend

; At the end of the install.
!macro customInstall
  ${If} ${Silent}
    Push $R0
    Push $R1
    ${GetParameters} $R0
    ClearErrors
    ${GetOptions} $R0 "/NODESKTOP" $R1
    ${If} ${Errors}
      ${If} $TraceFirstInstall == "1"
        Call CreateDesktopShortcut
      ${EndIf}
      !ifdef TRACE_DESKTOP_ON_UPDATE
        ${If} $TraceFirstInstall != "1"
          Call CreateDesktopShortcut
        ${EndIf}
      !endif
    ${EndIf}

    ClearErrors
    ${GetOptions} $R0 "/RELAUNCH" $R1
    ${IfNot} ${Errors}
      Exec '"$INSTDIR\trace.exe"'
    ${EndIf}
    Pop $R1
    Pop $R0
  ${EndIf}
!macroend

; During uninstall: remove only this channel's per-user Windows login autostart entry.
!macro customUnInstall
  !ifdef TRACE_AUTOSTART_VALUE
    DeleteRegValue HKCU "${TRACE_RUN_REGKEY}" "${TRACE_AUTOSTART_VALUE}"
    ClearErrors
  !endif
!macroend
