package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- very small in-memory session store (fine for a single-process admin
// panel; sessions are lost on server restart, which just forces a re-login) ---

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // cookie value -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

const sessionTTL = 12 * time.Hour
const sessionCookieName = "govpn_admin_session"

func (s *sessionStore) create() (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return tok, nil
}

func (s *sessionStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

type Admin struct {
	store           *StoreHolder
	sessions        *sessionStore
	adminUser       string
	adminPassSum    []byte // we just compare the raw password with constant time; no need for hashing complexity in this MVP
	adminPass       string
	onlineThreshold time.Duration
}

func NewAdmin(store *StoreHolder, user, pass string, onlineThreshold time.Duration) *Admin {
	return &Admin{store: store, sessions: newSessionStore(), adminUser: user, adminPass: pass, onlineThreshold: onlineThreshold}
}

func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomPassword() string {
	b := make([]byte, 9)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- middleware ---

func (a *Admin) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || !a.sessions.valid(c.Value) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// --- shared visual style ---
//
// Loosely inspired by modern SaaS admin consoles: dark purple/gradient
// sidebar with nav sections, light main area with rounded white cards
// in a grid. Pure CSS, no JS framework, no external fonts/icons.

const baseCSS = `
:root{--bg:#f6f5fa;--text-dark:#1a1523;--text-gray:#6b7280;--border:#e5e3ef;--card-bg:#ffffff;--accent1:#8b5cf6;--accent2:#d946ef}
*{box-sizing:border-box}
body{margin:0;font-family:-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;background:var(--bg);color:var(--text-dark);display:flex;min-height:100vh}

.sidebar{width:220px;background:linear-gradient(180deg,#140f1f,#1b1430 65%);color:#fff;display:flex;flex-direction:column;padding:22px 14px;position:fixed;top:0;bottom:0;left:0;overflow-y:auto}
.sidebar .logo{display:flex;align-items:center;gap:10px;margin-bottom:20px;padding:0 6px}
.sidebar .logo .mark{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,var(--accent1),var(--accent2));display:flex;align-items:center;justify-content:center;font-weight:700;font-size:12px;flex-shrink:0}
.sidebar .logo .name{font-weight:600;font-size:14px;line-height:1.2}
.sidebar .logo .name small{display:block;font-weight:400;color:#a9a3c0;font-size:11px}
.navsec{font-size:10px;letter-spacing:.08em;text-transform:uppercase;color:#786f94;margin:16px 8px 6px}
.navitem{display:block;padding:9px 10px;border-radius:8px;font-size:13px;color:#cfc9e0;text-decoration:none;margin-bottom:2px}
.navitem:hover{background:rgba(255,255,255,.06)}
.navitem.active{background:rgba(139,92,246,.22);color:#fff;font-weight:600}
.sidebar-foot{margin-top:auto;border-top:1px solid rgba(255,255,255,.08);padding-top:10px}
.sidebar-foot form{margin:0}
.sidebar-foot button{background:none;border:none;color:#cfc9e0;font-size:13px;cursor:pointer;padding:9px 10px;width:100%;text-align:left;border-radius:8px}
.sidebar-foot button:hover{background:rgba(255,255,255,.06);color:#fff}

.main{margin-left:220px;padding:36px 44px;flex:1;max-width:1180px;width:100%}
.pagehead h1{font-size:28px;margin:0 0 4px;font-weight:700}
.pagehead p{margin:0 0 20px;color:var(--text-gray);font-size:14px}

.flash{background:#dcfce7;color:#166534;padding:10px 14px;border-radius:8px;margin:0 0 18px;font-size:13px;font-family:ui-monospace,Menlo,Consolas,monospace}
.err{color:#b91c1c;font-size:13px}

.sectionhead{display:flex;align-items:center;gap:8px;margin:26px 0 12px;font-weight:600;font-size:14px}
.sectionhead .count{background:#ece9f7;color:#6d28d9;font-size:11px;padding:1px 9px;border-radius:99px;font-weight:600}

.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(230px,1fr));gap:14px}
.gcard{background:var(--card-bg);border:1px solid var(--border);border-radius:14px;padding:16px;position:relative;box-shadow:0 1px 2px rgba(20,15,30,.03)}
.gcard .top{display:flex;justify-content:space-between;align-items:flex-start}
.gcard .kind{font-size:10px;letter-spacing:.06em;text-transform:uppercase;color:#9a93ad;font-weight:700}
.gcard .rm{background:none;border:none;color:#b3adc4;font-size:15px;cursor:pointer;line-height:1;padding:3px 7px;border-radius:6px}
.gcard .rm:hover{background:#fee2e2;color:#b91c1c}
.gcard .icon{width:38px;height:38px;border-radius:10px;background:linear-gradient(135deg,var(--accent1),var(--accent2));margin:12px 0 10px;display:flex;align-items:center;justify-content:center;color:#fff;font-size:14px;font-weight:700}
.gcard .title{font-weight:600;font-size:14px;margin-bottom:6px;word-break:break-word}
.gcard .meta{font-size:11px;color:#9a93ad;font-family:ui-monospace,Menlo,Consolas,monospace;word-break:break-all}
.status{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--text-gray);margin-top:10px}
.dot{width:7px;height:7px;border-radius:50%;background:#9ca3af;flex-shrink:0}
.dot.on{background:#22c55e}

.panel{background:var(--card-bg);border:1px solid var(--border);border-radius:14px;padding:20px;margin-top:10px}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:10px;border-bottom:1px solid var(--border)}
th{color:#9a93ad;font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:.04em}
td.mono,.mono{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px;color:#55506a}
.pill{display:inline-block;padding:3px 9px;border-radius:99px;font-size:11px;background:#f1eff8;color:#6d28d9;font-weight:600}
.pill.ok{background:#dcfce7;color:#166534}

button{background:linear-gradient(135deg,var(--accent1),var(--accent2));color:#fff;border:none;padding:9px 16px;border-radius:8px;cursor:pointer;font-size:13px;font-weight:600}
button.secondary{background:#fff;color:var(--text-dark);border:1px solid var(--border)}
button.danger{background:#ef4444}
form.inline{display:inline}

input[type=text],input[type=password]{padding:9px 12px;border:1px solid var(--border);border-radius:8px;font-size:13px;background:#fff;color:var(--text-dark)}
.card{background:var(--card-bg);border:1px solid var(--border);border-radius:14px;padding:22px;max-width:420px}
.card label{display:block;margin-bottom:14px;font-size:13px;color:var(--text-gray)}
.card input[type=text],.card input[type=password]{display:block;width:100%;margin-top:6px}
.card input[type=checkbox]{width:auto;margin-right:6px;vertical-align:middle}

.newkey-form{display:flex;gap:10px;align-items:flex-end;flex-wrap:wrap;margin-bottom:16px}
.newkey-form label{font-size:12px;color:var(--text-gray);display:block}

a{color:#7c3aed;text-decoration:none}

.loginwrap{width:100%;display:flex;align-items:center;justify-content:center;min-height:100vh}
.logincard{background:#fff;border:1px solid var(--border);border-radius:16px;padding:36px;width:320px;box-shadow:0 10px 30px rgba(20,15,30,.08)}
.logincard .mark{width:40px;height:40px;border-radius:10px;background:linear-gradient(135deg,var(--accent1),var(--accent2));margin:0 auto 14px}
.logincard h1{text-align:center;font-size:20px;margin:0 0 20px}
`

// sidebarDef defines the {{template "sidebar"}} block shared by every
// logged-in page. Data passed to it just needs an "Active" field
// ("devices" or "backup") so the matching nav item gets highlighted.
const sidebarDef = `{{define "sidebar"}}
<div class="sidebar">
  <div class="logo"><div class="mark">GV</div><div class="name">govpn<small>mesh vpn</small></div></div>
  <div class="navsec">Malha</div>
  <a class="navitem{{if eq .Active "devices"}} active{{end}}" href="/admin">Dispositivos</a>
  <a class="navitem{{if eq .Active "backup"}} active{{end}}" href="/admin/backup">Backup</a>
  <div class="sidebar-foot">
    <form method="POST" action="/admin/logout"><button type="submit" class="secondary">Sair</button></form>
  </div>
</div>
{{end}}`

// --- login ---

var loginTpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - login</title>
<style>` + baseCSS + `</style></head>
<body><div class="loginwrap">
<div class="logincard">
  <div class="mark"></div>
  <h1>govpn</h1>
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
  <form method="POST" action="/admin/login">
    <label>Usuário<input type="text" name="user" autofocus></label>
    <label>Senha<input type="password" name="pass"></label>
    <button type="submit" style="width:100%">Entrar</button>
  </form>
</div>
</div></body></html>`))

func (a *Admin) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	loginTpl.Execute(w, map[string]string{})
}

func (a *Admin) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	user := r.FormValue("user")
	pass := r.FormValue("pass")
	if !constEq(user, a.adminUser) || !constEq(pass, a.adminPass) {
		w.WriteHeader(http.StatusUnauthorized)
		loginTpl.Execute(w, map[string]string{"Error": "usuário ou senha inválidos"})
		return
	}
	tok, err := a.sessions.create()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.sessions.destroy(c.Value)
	}
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// --- dashboard ---

type dashDevice struct {
	Hostname    string
	Initials    string
	IP          string
	PubKey      string
	PubKeyShort string
	Endpoint    string
	LastSeen    string
	LastSeenAgo string
	Online      bool
}

type dashKey struct {
	Key       string
	Note      string
	Reusable  bool
	Used      bool
	CreatedAt string
}

var dashTpl = template.Must(template.New("dash").Parse(sidebarDef + `
<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin</title>
<style>` + baseCSS + `</style></head>
<body>
{{template "sidebar" .}}
<div class="main">
<div class="pagehead"><h1>Dispositivos</h1><p>Máquinas registradas na sua malha govpn.</p></div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}

<div class="sectionhead">Dispositivos <span class="count">{{len .Devices}}</span></div>
<div class="grid">
{{range .Devices}}
<div class="gcard">
  <div class="top">
    <span class="kind">Mesh</span>
    <form class="inline" method="POST" action="/admin/devices/delete" onsubmit="return confirm('Remover {{.Hostname}} da malha?');">
      <input type="hidden" name="pub_key" value="{{.PubKey}}">
      <button type="submit" class="rm" title="Remover">&#10005;</button>
    </form>
  </div>
  <div class="icon">{{.Initials}}</div>
  <div class="title">{{.Hostname}}</div>
  <div class="meta">{{.IP}}<br>{{.PubKeyShort}}{{if .Endpoint}}<br>{{.Endpoint}}{{end}}</div>
  <div class="status"><span class="dot{{if .Online}} on{{end}}"></span>{{if .Online}}Online{{else}}Offline{{end}} &middot; {{.LastSeenAgo}}</div>
</div>
{{else}}
<p style="color:var(--text-gray);font-size:13px">Nenhum dispositivo registrado ainda.</p>
{{end}}
</div>

<div class="sectionhead">Auth keys <span class="count">{{len .Keys}}</span></div>
<div class="panel">
<form class="newkey-form" method="POST" action="/admin/keys/new">
  <div><label>Nota<input type="text" name="note" placeholder="ex: notebook do joão"></label></div>
  <div><label><input type="checkbox" name="reusable" value="1"> reutilizável</label></div>
  <button type="submit">Gerar nova chave</button>
</form>
<table>
<tr><th>Chave</th><th>Nota</th><th>Tipo</th><th>Status</th><th>Criada em</th><th></th></tr>
{{range .Keys}}
<tr>
  <td class="mono">{{.Key}}</td>
  <td>{{.Note}}</td>
  <td>{{if .Reusable}}reutilizável{{else}}uso único{{end}}</td>
  <td>{{if .Used}}<span class="pill">usada</span>{{else}}<span class="pill ok">disponível</span>{{end}}</td>
  <td>{{.CreatedAt}}</td>
  <td>
    <form class="inline" method="POST" action="/admin/keys/revoke" onsubmit="return confirm('Revogar esta chave?');">
      <input type="hidden" name="key" value="{{.Key}}">
      <button type="submit" class="danger">Revogar</button>
    </form>
  </td>
</tr>
{{else}}
<tr><td colspan="6">Nenhuma auth key criada ainda.</td></tr>
{{end}}
</table>
</div>

</div>
</body></html>`))

func fmtLastSeen(ts int64, threshold time.Duration) (abs, ago string, online bool) {
	if ts == 0 {
		return "", "nunca", false
	}
	t := time.Unix(ts, 0)
	d := time.Since(t)
	online = d < threshold
	return t.Format("2006-01-02 15:04:05"), humanizeAgo(d), online
}

func humanizeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return "agora mesmo"
	case d < time.Minute:
		return fmt.Sprintf("há %ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("há %dmin", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("há %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("há %dd", int(d.Hours()/24))
	}
}

func shortKey(k string) string {
	if len(k) <= 14 {
		return k
	}
	return k[:8] + "…" + k[len(k)-6:]
}

func initialsOf(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "?"
	}
	parts := strings.FieldsFunc(hostname, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' })
	if len(parts) == 0 {
		parts = []string{hostname}
	}
	r := []rune(strings.ToUpper(parts[0]))
	if len(parts) > 1 {
		r2 := []rune(strings.ToUpper(parts[1]))
		if len(r) > 0 && len(r2) > 0 {
			return string(r[0]) + string(r2[0])
		}
	}
	if len(r) >= 2 {
		return string(r[:2])
	}
	if len(r) == 1 {
		return string(r[0])
	}
	return "?"
}

func (a *Admin) handleDashboard(w http.ResponseWriter, r *http.Request) {
	devices, err := a.store.Get().ListAllDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	keys, err := a.store.Get().ListAuthKeys()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := struct {
		Active  string
		Devices []dashDevice
		Keys    []dashKey
		Flash   string
	}{Active: "devices"}

	for _, d := range devices {
		abs, ago, online := fmtLastSeen(d.LastSeen, a.onlineThreshold)
		hn := d.Hostname
		if hn == "" {
			hn = "(sem nome)"
		}
		data.Devices = append(data.Devices, dashDevice{
			Hostname: hn, Initials: initialsOf(hn), IP: d.IP, PubKey: d.PubKey, PubKeyShort: shortKey(d.PubKey), Endpoint: d.Endpoint,
			LastSeen: abs, LastSeenAgo: ago, Online: online,
		})
	}
	for _, k := range keys {
		data.Keys = append(data.Keys, dashKey{
			Key: k.Key, Note: k.Note, Reusable: k.Reusable, Used: k.Used,
			CreatedAt: time.Unix(k.CreatedAt, 0).Format("2006-01-02 15:04:05"),
		})
	}
	if msg := r.URL.Query().Get("flash"); msg != "" {
		data.Flash = msg
	}
	dashTpl.Execute(w, data)
}

func (a *Admin) handleNewKey(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	note := r.FormValue("note")
	reusable := r.FormValue("reusable") == "1"
	key, err := a.store.Get().CreateAuthKey(note, reusable)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin?flash="+template.URLQueryEscaper(fmt.Sprintf("nova auth key: %s", key)), http.StatusSeeOther)
}

func (a *Admin) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.FormValue("key")
	if err := a.store.Get().RevokeAuthKey(key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *Admin) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	pub := r.FormValue("pub_key")
	if err := a.store.Get().DeleteDevice(pub); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// --- backup page ---

var backupTpl = template.Must(template.New("backup").Parse(sidebarDef + `
<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - backup</title>
<style>` + baseCSS + `</style></head>
<body>
{{template "sidebar" .}}
<div class="main">
<div class="pagehead"><h1>Backup e restauração</h1><p>Cópias de segurança do banco de dados da malha.</p></div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}

<div class="sectionhead">Configuração</div>
<form method="POST" action="/admin/backup/settings" class="card">
  <label>Diretório de backup (caminho no servidor)
    <input type="text" name="dir" value="{{.Dir}}" placeholder="/home/lab/govpn/backups">
  </label>
  <label><input type="checkbox" name="enabled" value="1" {{if .Enabled}}checked{{end}}> backup automático habilitado</label>
  <label>Intervalo (minutos)
    <input type="text" name="interval" value="{{.IntervalMinutes}}">
  </label>
  <label>Retenção: manter apenas os N backups mais recentes (0 = manter todos)
    <input type="text" name="retention" value="{{.RetentionCount}}">
  </label>
  <button type="submit">Salvar configuração</button>
</form>

<form method="POST" action="/admin/backup/now" style="margin-top:14px">
  <button type="submit" class="secondary">Fazer backup agora</button>
</form>

<div class="sectionhead">Backups disponíveis <span class="count">{{len .Backups}}</span></div>
<div class="panel">
<table>
<tr><th>Arquivo</th><th>Tamanho</th><th>Criado em</th><th></th></tr>
{{range .Backups}}
<tr>
  <td class="mono">{{.Name}}</td>
  <td>{{.SizeH}}</td>
  <td>{{.ModTimeH}}</td>
  <td>
    <form class="inline" method="POST" action="/admin/backup/restore" onsubmit="return confirm('Restaurar {{.Name}}? Isso substitui o banco de dados atual e derruba todas as sessões/registros feitos depois deste backup.');">
      <input type="hidden" name="file" value="{{.Name}}">
      <button type="submit" class="secondary">Restaurar</button>
    </form>
    <form class="inline" method="POST" action="/admin/backup/delete" onsubmit="return confirm('Apagar o backup {{.Name}}? Essa ação não pode ser desfeita.');">
      <input type="hidden" name="file" value="{{.Name}}">
      <button type="submit" class="danger">Apagar</button>
    </form>
  </td>
</tr>
{{else}}
<tr><td colspan="4">Nenhum backup encontrado{{if .Dir}} em {{.Dir}}{{end}}.</td></tr>
{{end}}
</table>
</div>

</div>
</body></html>`))

type backupFileView struct {
	Name     string
	SizeH    string
	ModTimeH string
}

func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *Admin) handleBackupPage(w http.ResponseWriter, r *http.Request) {
	settings := a.store.GetBackupSettings()
	backups, err := a.store.ListBackups(settings.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Active          string
		Dir             string
		Enabled         bool
		IntervalMinutes int
		RetentionCount  int
		Backups         []backupFileView
		Flash           string
		Error           string
	}{
		Active: "backup",
		Dir:    settings.Dir, Enabled: settings.Enabled, IntervalMinutes: settings.IntervalMinutes,
		RetentionCount: settings.RetentionCount,
	}
	for _, b := range backups {
		data.Backups = append(data.Backups, backupFileView{
			Name: b.Name, SizeH: humanizeBytes(b.Size), ModTimeH: b.ModTime.Format("2006-01-02 15:04:05"),
		})
	}
	if msg := r.URL.Query().Get("flash"); msg != "" {
		data.Flash = msg
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		data.Error = msg
	}
	backupTpl.Execute(w, data)
}

func (a *Admin) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	dir := strings.TrimSpace(r.FormValue("dir"))
	enabled := r.FormValue("enabled") == "1"
	interval := 60
	fmt.Sscanf(r.FormValue("interval"), "%d", &interval)
	if interval < 1 {
		interval = 1
	}
	var retention int
	fmt.Sscanf(r.FormValue("retention"), "%d", &retention)
	if retention < 0 {
		retention = 0
	}
	if err := a.store.SetBackupSettings(dir, enabled, interval, retention); err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("configuração salva"), http.StatusSeeOther)
}

func (a *Admin) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	settings := a.store.GetBackupSettings()
	path, err := a.store.RunBackupNow(settings.Dir)
	if err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("backup criado: "+path), http.StatusSeeOther)
}

func (a *Admin) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("file")
	settings := a.store.GetBackupSettings()
	if settings.Dir == "" || name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper("arquivo de backup inválido"), http.StatusSeeOther)
		return
	}
	full := filepath.Join(settings.Dir, name)
	// defense in depth: resolved path must still live inside the backup dir
	cleanDir := filepath.Clean(settings.Dir)
	if rel, err := filepath.Rel(cleanDir, full); err != nil || strings.HasPrefix(rel, "..") {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper("caminho de backup inválido"), http.StatusSeeOther)
		return
	}
	if err := a.store.Restore(full); err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("banco restaurado a partir de "+name), http.StatusSeeOther)
}

func (a *Admin) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("file")
	settings := a.store.GetBackupSettings()
	if err := a.store.DeleteBackup(settings.Dir, name); err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("backup apagado: "+name), http.StatusSeeOther)
}

// RegisterRoutes wires every /admin* route onto the given mux.
func (a *Admin) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.handleLoginSubmit(w, r)
			return
		}
		a.handleLoginPage(w, r)
	})
	mux.HandleFunc("/admin/logout", a.handleLogout)
	mux.HandleFunc("/admin", a.requireLogin(a.handleDashboard))
	mux.HandleFunc("/admin/backup", a.requireLogin(a.handleBackupPage))
	mux.HandleFunc("/admin/backup/settings", a.requireLogin(a.handleBackupSettings))
	mux.HandleFunc("/admin/backup/now", a.requireLogin(a.handleBackupNow))
	mux.HandleFunc("/admin/backup/restore", a.requireLogin(a.handleBackupRestore))
	mux.HandleFunc("/admin/backup/delete", a.requireLogin(a.handleBackupDelete))
	mux.HandleFunc("/admin/keys/new", a.requireLogin(a.handleNewKey))
	mux.HandleFunc("/admin/keys/revoke", a.requireLogin(a.handleRevokeKey))
	mux.HandleFunc("/admin/devices/delete", a.requireLogin(a.handleDeleteDevice))
}
