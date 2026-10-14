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

// --- login ---

var loginTpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - login</title>
<style>` + baseCSS + `</style></head>
<body><div class="wrap">
<h1>govpn</h1>
<form method="POST" action="/admin/login" class="card">
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
  <label>Usuário<input type="text" name="user" autofocus></label>
  <label>Senha<input type="password" name="pass"></label>
  <button type="submit">Entrar</button>
</form>
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

const baseCSS = `
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#0f1115;color:#e7e9ee;margin:0}
.wrap{max-width:980px;margin:40px auto;padding:0 20px}
h1{font-weight:600}
h2{font-size:16px;color:#9aa4b2;text-transform:uppercase;letter-spacing:.05em;margin-top:40px}
.card{background:#171a21;border:1px solid #262b36;border-radius:10px;padding:24px;max-width:340px;margin:60px auto}
.card label{display:block;margin-bottom:14px;font-size:13px;color:#9aa4b2}
.card input{display:block;width:100%;padding:8px;margin-top:6px;background:#0f1115;border:1px solid #333;border-radius:6px;color:#e7e9ee;box-sizing:border-box}
button{background:#3b82f6;color:white;border:none;padding:9px 16px;border-radius:6px;cursor:pointer;font-size:14px}
button.danger{background:#ef4444}
.err{color:#ef4444;font-size:13px}
table{width:100%;border-collapse:collapse;margin-top:12px;font-size:13px}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #262b36}
th{color:#9aa4b2;font-weight:500}
td.mono,.mono{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px;color:#c7cdd6}
.topbar{display:flex;justify-content:space-between;align-items:center}
.pill{display:inline-block;padding:2px 8px;border-radius:99px;font-size:11px;background:#262b36;color:#9aa4b2}
.pill.ok{background:#14532d;color:#86efac}
form.inline{display:inline}
.newkey-form{display:flex;gap:10px;align-items:flex-end;margin-top:12px;flex-wrap:wrap}
.newkey-form label{font-size:12px;color:#9aa4b2}
.newkey-form input[type=text]{padding:7px;background:#0f1115;border:1px solid #333;border-radius:6px;color:#e7e9ee}
a{color:#60a5fa}
.flash{background:#14532d;color:#86efac;padding:10px 14px;border-radius:6px;margin:14px 0;font-family:ui-monospace,monospace;font-size:13px}
`

type dashDevice struct {
	Hostname    string
	IP          string
	PubKey      string
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

var dashTpl = template.Must(template.New("dash").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin</title>
<style>` + baseCSS + `</style></head>
<body><div class="wrap">
<div class="topbar">
  <h1>govpn admin</h1>
  <div>
    <a href="/admin/backup"><button type="button">Backup</button></a>
    <form class="inline" method="POST" action="/admin/logout"><button type="submit">Sair</button></form>
  </div>
</div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}

<h2>Dispositivos ({{len .Devices}})</h2>
<table>
<tr><th>Hostname</th><th>IP</th><th>Endpoint</th><th>Última atividade</th><th>Chave pública</th><th></th></tr>
{{range .Devices}}
<tr>
  <td>{{.Hostname}}</td>
  <td class="mono">{{.IP}}</td>
  <td class="mono">{{if .Endpoint}}{{.Endpoint}}{{else}}—{{end}}</td>
  <td>{{.LastSeenAgo}} <span class="mono" style="color:#6b7280">({{.LastSeen}})</span> {{if .Online}}<span class="pill ok">online</span>{{else}}<span class="pill">offline</span>{{end}}</td>
  <td class="mono">{{.PubKey}}</td>
  <td>
    <form class="inline" method="POST" action="/admin/devices/delete" onsubmit="return confirm('Remover {{.Hostname}} da malha?');">
      <input type="hidden" name="pub_key" value="{{.PubKey}}">
      <button type="submit" class="danger">Remover</button>
    </form>
  </td>
</tr>
{{else}}
<tr><td colspan="6">Nenhum dispositivo registrado ainda.</td></tr>
{{end}}
</table>

<h2>Auth keys</h2>
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

</div></body></html>`))

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
		Devices []dashDevice
		Keys    []dashKey
		Flash   string
	}{}

	for _, d := range devices {
		abs, ago, online := fmtLastSeen(d.LastSeen, a.onlineThreshold)
		hn := d.Hostname
		if hn == "" {
			hn = "(sem nome)"
		}
		data.Devices = append(data.Devices, dashDevice{
			Hostname: hn, IP: d.IP, PubKey: d.PubKey, Endpoint: d.Endpoint,
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

var backupTpl = template.Must(template.New("backup").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - backup</title>
<style>` + baseCSS + `</style></head>
<body><div class="wrap">
<div class="topbar">
  <h1>Backup e restauração</h1>
  <a href="/admin"><button type="button">&larr; Voltar</button></a>
</div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}

<h2>Configuração</h2>
<form method="POST" action="/admin/backup/settings" class="card" style="max-width:460px;margin:20px 0">
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

<form method="POST" action="/admin/backup/now">
  <button type="submit">Fazer backup agora</button>
</form>

<h2>Backups disponíveis ({{len .Backups}})</h2>
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
      <button type="submit">Restaurar</button>
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

</div></body></html>`))

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
		Dir             string
		Enabled         bool
		IntervalMinutes int
		RetentionCount  int
		Backups         []backupFileView
		Flash           string
		Error           string
	}{
		Dir: settings.Dir, Enabled: settings.Enabled, IntervalMinutes: settings.IntervalMinutes,
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
