; govpn Windows installer (NSIS)
; Builds with: makensis govpn-installer.nsi  ->  govpn-setup.exe
;
; Installs the system-tray GUI client (govpn-client-gui.exe), the CLI
; (govpn-client.exe, handy for troubleshooting from a terminal) and the
; Wintun driver DLL it needs to create the VPN network interface.

!define APPNAME "govpn"
!define COMPANYNAME "govpn"
!define DESCRIPTION "Mesh VPN client (WireGuard-based)"
!define VERSIONMAJOR 1
!define VERSIONMINOR 0
!define VERSIONBUILD 0
!define HELPURL "https://github.com/"
!define UPDATEURL "https://github.com/"
!define ABOUTURL "https://github.com/"
!define INSTALLSIZE 40000

RequestExecutionLevel admin ; creating the WireGuard TUN device needs admin, same as the official WireGuard client
InstallDir "$PROGRAMFILES64\govpn"
Name "${APPNAME}"
OutFile "..\bin\govpn-setup.exe"

!include "MUI2.nsh"
!include "LogicLib.nsh"

!define MUI_ABORTWARNING
!define MUI_ICON "assets\icon_connected.ico"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\govpn-client-gui.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Iniciar o govpn agora (fica na bandeja do sistema)"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "PortugueseBR"

Function .onInit
	SetShellVarContext all
FunctionEnd

Section "govpn (obrigatório)" SecCore
	SectionIn RO
	SetOutPath "$INSTDIR"

	; stop a previously running instance before overwriting its files
	nsExec::Exec 'taskkill /IM govpn-client-gui.exe /F'

	File "..\bin\govpn-client-gui.exe"
	File "..\bin\govpn-client.exe"
	File "thirdparty\wintun.dll"
	File "..\README.md"

	CreateDirectory "$SMPROGRAMS\govpn"
	CreateShortcut "$SMPROGRAMS\govpn\govpn.lnk" "$INSTDIR\govpn-client-gui.exe"
	CreateShortcut "$SMPROGRAMS\govpn\Desinstalar govpn.lnk" "$INSTDIR\uninstall.exe"
	CreateShortcut "$DESKTOP\govpn.lnk" "$INSTDIR\govpn-client-gui.exe"

	WriteUninstaller "$INSTDIR\uninstall.exe"

	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "DisplayName" "${APPNAME}"
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "InstallLocation" "$\"$INSTDIR$\""
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "Publisher" "${COMPANYNAME}"
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "HelpLink" "${HELPURL}"
	WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "DisplayVersion" "${VERSIONMAJOR}.${VERSIONMINOR}.${VERSIONBUILD}"
	WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "NoModify" 1
	WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "NoRepair" 1
	WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}" "EstimatedSize" ${INSTALLSIZE}
SectionEnd

Section "Uninstall"
	SetShellVarContext all
	nsExec::Exec 'taskkill /IM govpn-client-gui.exe /F'

	Delete "$INSTDIR\govpn-client-gui.exe"
	Delete "$INSTDIR\govpn-client.exe"
	Delete "$INSTDIR\wintun.dll"
	Delete "$INSTDIR\README.md"
	Delete "$INSTDIR\uninstall.exe"
	RMDir "$INSTDIR"

	Delete "$SMPROGRAMS\govpn\govpn.lnk"
	Delete "$SMPROGRAMS\govpn\Desinstalar govpn.lnk"
	RMDir "$SMPROGRAMS\govpn"
	Delete "$DESKTOP\govpn.lnk"

	DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Run\govpn-client-gui"
	DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"

	MessageBox MB_YESNO|MB_ICONQUESTION "Remover também o perfil salvo (chaves, IP atribuído) em %USERPROFILE%\.govpn?" IDNO skip_profile
		RMDir /r "$PROFILE\.govpn"
	skip_profile:
SectionEnd
