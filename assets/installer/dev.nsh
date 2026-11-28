; The dev channel's installer hooks: see shortcuts.nsh.
!define TRACE_SHORTCUT "TRACE Dev"
!define TRACE_AUTOSTART_VALUE "io.github.chialecode.trace-dev"
; Every silent over-install makes the desktop shortcut again.
!define TRACE_DESKTOP_ON_UPDATE
!include "${__FILEDIR__}\shortcuts.nsh"
