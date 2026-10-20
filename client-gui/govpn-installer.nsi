; govpn Windows installer (NSIS)
; Builds with: makensis govpn-installer.nsi  ->  govpn-setup.exe
;
; Installs the system-tray GUI client (govpn-client-gui.exe), the CLI
; (govpn-client.exe, handy for troubleshooting from a terminal) and the
; Wintun driver DLL it needs to create the VPN network interface.
;
; Also drops an editable config.yml in ProgramData (server/authkey/
; hostname) and a Startup-folder shortcut so the tray client connects
; and comes back up on its own after every boot/login — no manual
; "configure and click connect" step needed.

!define APPNAME "govpn"
!define COMPANYNAME "govpn"
!define DESCRIPTION "Mesh VPN client (WireGuard-based)"
!define VERSIONMAJOR 1
!define VERSIONMINOR 1
!define VERSIONBUILD 0
!define HELPURL "https://github.com/"
!define UPDATEURL "https://github.com/"
!define ABOUTURL "https://github.com/"
!define INSTALLSIZE 40000

Var CONFIGDIR

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
!define MUI_FINISHPAGE_RUN "notepad.exe"
!define MUI_FINISHPAGE_RUN_PARAMETERS "$CONFIGDIR\config.yml"
!define MUI_FINISHPAGE_RUN_TEXT "Abrir config.yml agora para preencher servidor e auth key"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "PortugueseBR"

Function .onInit
	SetShellVarContext all
	ReadEnvStr $CONFIGDIR "PROGRAMDATA"
	StrCpy $CONFIGDIR "$CONFIGDIR\govpn"
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

	; Config file (server/authkey/hostname), editable in Notepad. Never
	; overwritten on upgrade/reinstall so an already-filled-in key isn't
	; wiped out.
	CreateDirectory "$CONFIGDIR"
	${IfNot} ${FileExists} "$CONFIGDIR\config.yml"
		FileOpen $0 "$CONFIGDIR\config.yml" w
		FileWrite $0 "# Configuracao do cliente govpn.$\r$\n"
		FileWrite $0 "# Preencha server e authkey (gerados no painel admin do servidor,$\r$\n"
		FileWrite $0 "# em /admin) e reinicie o govpn (clique com o botao direito no$\r$\n"
		FileWrite $0 "# icone da bandeja > Reconectar agora, ou simplesmente espere$\r$\n"
		FileWrite $0 "# ~15s: ele rele este arquivo sozinho).$\r$\n"
		FileWrite $0 "#$\r$\n"
		FileWrite $0 "# authkey so e usada uma vez, no primeiro registro. Depois disso$\r$\n"
		FileWrite $0 "# o dispositivo fica identificado pelo estado salvo em$\r$\n"
		FileWrite $0 "# %USERPROFILE%\.govpn\state.json.$\r$\n"
		FileWrite $0 "#$\r$\n"
		FileWrite $0 "# O servidor fala HTTPS por padrao (certificado autoassinado, a$\r$\n"
		FileWrite $0 "# menos que o administrador tenha configurado um de verdade). Se$\r$\n"
		FileWrite $0 "# for esse o caso, descomente insecure_tls abaixo.$\r$\n"
		FileWrite $0 "$\r$\n"
		FileWrite $0 "server: $\"https://SEU_SERVIDOR:28443$\"$\r$\n"
		FileWrite $0 "authkey: $\"SUA_AUTH_KEY$\"$\r$\n"
		FileWrite $0 "hostname: $\"$\"$\r$\n"
		FileWrite $0 "iface: $\"govpn0$\"$\r$\n"
		FileWrite $0 "poll_interval: $\"10s$\"$\r$\n"
		FileWrite $0 "# insecure_tls: true$\r$\n"
		FileClose $0
	${EndIf}

	; Auto-start: a Scheduled Task (not a plain Startup-folder shortcut)
	; is required here — creating the WireGuard/Wintun network adapter
	; needs an elevated (Administrator) token, and a normal Startup
	; shortcut launches as the logged-in user's regular, non-elevated
	; token ("Acesso negado" / Access Denied when the adapter is
	; created). A Scheduled Task with RunLevel=Highest launches with a
	; full admin token straight away, with NO UAC prompt on every boot,
	; and still runs inside the interactive user's own session so the
	; tray icon is visible — the standard trick used by background
	; tools that need elevation at logon without nagging the user.
	nsExec::Exec 'schtasks /create /tn "govpn-client-gui" /tr "\"$INSTDIR\govpn-client-gui.exe\"" /sc onlogon /rl highest /f'

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
	ReadEnvStr $CONFIGDIR "PROGRAMDATA"
	StrCpy $CONFIGDIR "$CONFIGDIR\govpn"
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
	nsExec::Exec 'schtasks /delete /tn "govpn-client-gui" /f'

	DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Run\govpn-client-gui"
	DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"

	MessageBox MB_YESNO|MB_ICONQUESTION "Remover também a configuração ($CONFIGDIR) e o perfil salvo (chaves, IP atribuído) em %USERPROFILE%\.govpn?" IDNO skip_profile
		RMDir /r "$CONFIGDIR"
		RMDir /r "$PROFILE\.govpn"
	skip_profile:
SectionEnd
