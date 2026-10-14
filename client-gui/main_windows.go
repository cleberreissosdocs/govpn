//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/getlantern/systray"
	"github.com/lxn/walk"
	declarative "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"govpn/client/core"
)

//go:embed assets/icon_connected.ico
var iconConnected []byte

//go:embed assets/icon_disconnected.ico
var iconDisconnected []byte

var (
	eng         *core.Engine
	mStatus     *systray.MenuItem
	mConnect    *systray.MenuItem
	mDisconnect *systray.MenuItem
	mProfile    *systray.MenuItem
	mAutostart  *systray.MenuItem
	mOpenConfig *systray.MenuItem
	mQuit       *systray.MenuItem
)

const autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const autostartValueName = "govpn-client-gui"

// uiRun runs fn on a freshly spawned goroutine pinned to its own OS
// thread for its whole lifetime. Win32 windows are thread-affine: a
// window (and every message sent to it, including button clicks) must
// be created and pumped from the SAME OS thread for its entire life.
// Go goroutines can otherwise migrate between OS threads at any
// scheduling point, which silently breaks the message loop and makes
// dialogs look "dead" (nothing you click does anything). Every walk.*
// call in this file goes through this helper for that reason.
func uiRun(fn func()) {
	go func() {
		runtime.LockOSThread()
		fn()
	}()
}

func main() {
	// Single-instance guard: without this, each launch (desktop icon,
	// autostart, a stray double-click) stacks up a brand-new tray icon
	// and a brand-new (invisible, stuck-behind-everything) profile
	// dialog, which is exactly the kind of mess that looks like "nothing
	// happens when I click the menu".
	mutexName, _ := windows.UTF16PtrFromString(`Global\govpn-client-gui-singleton`)
	h, err := windows.CreateMutex(nil, false, mutexName)
	if err == nil && windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		walk.MsgBox(nil, "govpn", "O govpn já está em execução (veja o ícone na bandeja do sistema).", walk.MsgBoxIconInformation)
		return
	}
	_ = h

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("")
	systray.SetTooltip("govpn - desconectado")

	mStatus = systray.AddMenuItem("Desconectado", "estado atual")
	mStatus.Disable()
	systray.AddSeparator()
	mConnect = systray.AddMenuItem("Conectar", "Conectar à malha govpn")
	mDisconnect = systray.AddMenuItem("Desconectar", "Desconectar da malha govpn")
	mDisconnect.Disable()
	systray.AddSeparator()
	mProfile = systray.AddMenuItem("Perfil...", "Configurar servidor e auth key")
	mAutostart = systray.AddMenuItem("Iniciar com o Windows", "Inicia o govpn automaticamente no login")
	if autostartEnabled() {
		mAutostart.Check()
	}
	mOpenConfig = systray.AddMenuItem("Abrir pasta de configuração", "")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Sair", "Fechar o govpn")

	eng = core.NewEngine("govpn0", 10*time.Second)
	eng.OnStateChange = updateUI
	eng.OnLog = func(s string) { appendLog(s) }

	if eng.TryLoadSaved() {
		go func() {
			if err := eng.Connect(); err != nil {
				uiRun(func() {
					walk.MsgBox(nil, "govpn", "Falha ao conectar automaticamente:\n"+err.Error(), walk.MsgBoxIconError)
				})
			}
		}()
	} else {
		uiRun(showProfileDialog)
	}

	go menuLoop()
}

func menuLoop() {
	for {
		select {
		case <-mConnect.ClickedCh:
			handleConnectClick()
		case <-mDisconnect.ClickedCh:
			if err := eng.Disconnect(); err != nil {
				uiRun(func() { walk.MsgBox(nil, "govpn", err.Error(), walk.MsgBoxIconError) })
			}
		case <-mProfile.ClickedCh:
			uiRun(showProfileDialog)
		case <-mAutostart.ClickedCh:
			toggleAutostart()
		case <-mOpenConfig.ClickedCh:
			openConfigDir()
		case <-mQuit.ClickedCh:
			eng.Disconnect()
			systray.Quit()
			return
		}
	}
}

func handleConnectClick() {
	if !eng.HasProfile() {
		uiRun(showProfileDialog)
		return
	}
	if err := eng.Connect(); err != nil {
		uiRun(func() { walk.MsgBox(nil, "govpn", "Falha ao conectar:\n"+err.Error(), walk.MsgBoxIconError) })
	}
}

func updateUI(connected bool) {
	if connected {
		systray.SetIcon(iconConnected)
		st := eng.State()
		ip := ""
		if st != nil {
			ip = st.AssignedIP
		}
		systray.SetTooltip("govpn - conectado (" + ip + ")")
		mStatus.SetTitle("Conectado: " + ip)
		mConnect.Disable()
		mDisconnect.Enable()
	} else {
		systray.SetIcon(iconDisconnected)
		systray.SetTooltip("govpn - desconectado")
		mStatus.SetTitle("Desconectado")
		mConnect.Enable()
		mDisconnect.Disable()
	}
}

func onExit() {}

func appendLog(line string) {
	dir, err := core.StateDir()
	if err != nil {
		return
	}
	os.MkdirAll(dir, 0700)
	f, err := os.OpenFile(filepath.Join(dir, "gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

func openConfigDir() {
	dir, err := core.StateDir()
	if err != nil {
		return
	}
	os.MkdirAll(dir, 0700)
	// explorer.exe is always present on Windows; ignore errors here (e.g.
	// path doesn't exist yet), there's nothing actionable to show the user.
	runCmd("explorer.exe", dir)
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autostartValueName)
	return err == nil
}

func toggleAutostart() {
	if autostartEnabled() {
		k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
		if err == nil {
			k.DeleteValue(autostartValueName)
			k.Close()
		}
		mAutostart.Uncheck()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		uiRun(func() {
			walk.MsgBox(nil, "govpn", "Não foi possível configurar início automático:\n"+err.Error(), walk.MsgBoxIconError)
		})
		return
	}
	defer k.Close()
	if err := k.SetStringValue(autostartValueName, `"`+exe+`"`); err != nil {
		uiRun(func() { walk.MsgBox(nil, "govpn", err.Error(), walk.MsgBoxIconError) })
		return
	}
	mAutostart.Check()
}

func showProfileDialog() {
	appendLog("showProfileDialog: entrou")
	defer func() {
		if r := recover(); r != nil {
			appendLog(fmt.Sprintf("showProfileDialog: PANIC recuperado: %v", r))
		}
	}()
	st := eng.State()
	defaultServer, defaultHost := "", ""
	if st != nil {
		defaultServer = st.ServerURL
		defaultHost = st.Hostname
	}
	if defaultHost == "" {
		defaultHost, _ = os.Hostname()
	}

	var dlg *walk.Dialog
	var serverEdit, authEdit, hostEdit *walk.LineEdit
	var statusLabel *walk.Label

	cnt, err := declarative.Dialog{
		AssignTo: &dlg,
		Title:    "govpn - perfil",
		MinSize:  declarative.Size{Width: 420, Height: 260},
		Layout:   declarative.VBox{},
		Children: []declarative.Widget{
			declarative.Label{Text: "Servidor (ex: http://192.168.1.10:8443)"},
			declarative.LineEdit{AssignTo: &serverEdit, Text: defaultServer},
			declarative.Label{Text: "Auth key (gerada no painel admin do servidor)"},
			declarative.LineEdit{AssignTo: &authEdit},
			declarative.Label{Text: "Nome deste dispositivo"},
			declarative.LineEdit{AssignTo: &hostEdit, Text: defaultHost},
			declarative.Label{AssignTo: &statusLabel, Text: ""},
			declarative.Composite{
				Layout: declarative.HBox{},
				Children: []declarative.Widget{
					declarative.PushButton{
						Text: "Salvar e conectar",
						OnClicked: func() {
							server := serverEdit.Text()
							authKey := authEdit.Text()
							hostname := hostEdit.Text()
							statusLabel.SetText("Conectando...")
							// Register()/Connect() do network I/O; running them
							// inline here would freeze this dialog's message
							// loop (it's pinned to this one OS thread) until the
							// request finishes. Do the work on a plain
							// goroutine and marshal the result back with
							// Synchronize, which is the only safe way to touch
							// walk widgets from another goroutine.
							go func() {
								var err error
								if authKey == "" && eng.HasProfile() {
									// no new auth key given: just (re)connect
									// with whatever profile is already saved.
									err = eng.Connect()
								} else {
									eng.Disconnect()
									if err = eng.RegisterNew(server, authKey, hostname); err == nil {
										err = eng.Connect()
									}
								}
								dlg.Synchronize(func() {
									if err != nil {
										statusLabel.SetText("Erro: " + err.Error())
										return
									}
									dlg.Accept()
								})
							}()
						},
					},
					declarative.PushButton{
						Text: "Cancelar",
						OnClicked: func() {
							dlg.Cancel()
						},
					},
				},
			},
		},
	}.Run(nil)
	appendLog(fmt.Sprintf("profile dialog closed: result=%d err=%v", cnt, err))
}

func runCmd(name string, args ...string) {
	exec.Command(name, args...).Start()
}
