Unicode true
RequestExecutionLevel user
!include "MUI2.nsh"
!include "x64.nsh"

!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\QuailCrate"
Name "QuailCrate"
OutFile "${OUTPUT}"
InstallDir "$PROFILE\QuailCrate"
SetCompressor /SOLID lzma
VIProductVersion "${FILEVERSION}"
VIAddVersionKey "ProductName" "QuailCrate"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "QuailCrate Setup"
VIAddVersionKey "LegalCopyright" "SuperQuail"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

LangString NeedsX64 ${LANG_ENGLISH} "QuailCrate requires 64-bit Windows."
LangString NeedsX64 ${LANG_SIMPCHINESE} "QuailCrate 需要 64 位 Windows。"
LangString SetupFailed ${LANG_ENGLISH} "Could not update the installation. Close QuailCrate from its tray menu if it is running."
LangString SetupFailed ${LANG_SIMPCHINESE} "无法更新安装文件。如 QuailCrate 正在运行，请通过托盘菜单退出。"

Function .onInit
    ${IfNot} ${RunningX64}
        MessageBox MB_OK|MB_ICONSTOP "$(NeedsX64)" /SD IDOK
        SetErrorLevel 1
        Abort
    ${EndIf}
    SetRegView 64
    ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
    ${If} $0 != ""
        StrCpy $INSTDIR $0
    ${EndIf}
FunctionEnd

!macro Deploy operation
    SetOutPath "$PLUGINSDIR"
    File "deploy.ps1"
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\deploy.ps1" -Operation ${operation} -InstallDir "$INSTDIR" -Version "${VERSION}"'
    Pop $0
    Pop $1
    ${If} $0 != 0
        DetailPrint "$1"
        MessageBox MB_OK|MB_ICONSTOP "$(SetupFailed)$\r$\n$1" /SD IDOK
        SetErrorLevel 1
        Abort
    ${EndIf}
!macroend

Section "Install"
    InitPluginsDir
    SetOutPath "$PLUGINSDIR\payload"
    File /r "${STAGE}\*.*"
    WriteUninstaller "$PLUGINSDIR\payload\Uninstall.exe"
    !insertmacro Deploy Install
SectionEnd

Section "Uninstall"
    InitPluginsDir
    !insertmacro Deploy Uninstall
SectionEnd
