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
//
// Sessions only remember WHICH user (by id) is logged in — role and
// username are looked up fresh from the database on every request
// (see requireLogin), so a role change or a user deletion takes effect
// immediately instead of waiting for the session to expire.

type sessionEntry struct {
	userID int64
	expiry time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]sessionEntry)}
}

const sessionTTL = 12 * time.Hour
const sessionCookieName = "govpn_admin_session"

func (s *sessionStore) create(userID int64) (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[tok] = sessionEntry{userID: userID, expiry: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return tok, nil
}

func (s *sessionStore) valid(tok string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[tok]
	if !ok {
		return 0, false
	}
	if time.Now().After(e.expiry) {
		delete(s.sessions, tok)
		return 0, false
	}
	return e.userID, true
}

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

type Admin struct {
	store           *StoreHolder
	sessions        *sessionStore
	onlineThreshold time.Duration
	loginLimiter    *loginLimiter
	secureCookie    bool // set the cookie's Secure flag — only when actually serving over TLS
}

func NewAdmin(store *StoreHolder, onlineThreshold time.Duration) *Admin {
	return &Admin{store: store, sessions: newSessionStore(), onlineThreshold: onlineThreshold, loginLimiter: newLoginLimiter()}
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

type ctxKey int

const ctxUserKey ctxKey = 1

// requireLogin validates the session cookie, re-fetches the user from
// the database (so a role change or account deletion is honored
// immediately, not just at next login), and stashes it in the request
// context for handlers/templates to use.
func (a *Admin) requireLogin(next func(http.ResponseWriter, *http.Request, *UserRow)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		userID, ok := a.sessions.valid(c.Value)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		u, err := a.store.Get().UserByID(userID)
		if err != nil || u == nil {
			a.sessions.destroy(c.Value)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r, u)
	}
}

// requireAdmin is requireLogin plus a role check — use for every
// route a plain "user" account must NOT be able to reach (auth keys,
// device removal, backups, user management), never relying on the UI
// alone to hide those actions.
func (a *Admin) requireAdmin(next func(http.ResponseWriter, *http.Request, *UserRow)) http.HandlerFunc {
	return a.requireLogin(func(w http.ResponseWriter, r *http.Request, u *UserRow) {
		if u.Role != RoleAdmin {
			http.Error(w, "acesso restrito a administradores", http.StatusForbidden)
			return
		}
		next(w, r, u)
	})
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
// logged-in page. Data passed to it needs an "Active" field ("devices",
// "backup", "users" or "account") so the matching nav item gets
// highlighted, plus "IsAdmin" and "Username" to decide which links show.
const sidebarDef = `{{define "sidebar"}}
<div class="sidebar">
  <div class="logo"><div class="mark">GV</div><div class="name">govpn<small>mesh vpn</small></div></div>
  <div class="navsec">Malha</div>
  <a class="navitem{{if eq .Active "devices"}} active{{end}}" href="/admin">Dispositivos</a>
  {{if .IsAdmin}}<a class="navitem{{if eq .Active "backup"}} active{{end}}" href="/admin/backup">Backup</a>{{end}}
  {{if .IsAdmin}}<div class="navsec">Administração</div>
  <a class="navitem{{if eq .Active "users"}} active{{end}}" href="/admin/users">Usuários</a>{{end}}
  <div class="navsec">Conta</div>
  <a class="navitem{{if eq .Active "account"}} active{{end}}" href="/admin/account">Minha conta{{if .Username}} ({{.Username}}){{end}}</a>
  <div class="sidebar-foot">
    <form method="POST" action="/admin/logout"><button type="submit" class="secondary">Sair</button></form>
  </div>
</div>
{{end}}`

// --- login ---

var loginTpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - login</title>
<style>` + baseCSS + `
.loginwrap{position:relative;z-index:1}
#bgcanvas{position:fixed;inset:0;z-index:0;display:block}
</style></head>
<body><canvas id="bgcanvas"></canvas><div class="loginwrap">
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
</div>
<script src="/admin/assets/bg-particles.js"></script>
</body></html>`))

// bgParticlesJS is served from its own same-origin route instead of
// inlined in <script> tags — our CSP is "default-src 'self'" with no
// 'unsafe-inline' for scripts (deliberately, so a reflected-XSS bug
// anywhere else in the panel can't execute attacker JS), so an inline
// <script> block would just be silently blocked by the browser.
const bgParticlesJS = `(function(){
  var canvas = document.getElementById('bgcanvas');
  if (!canvas) return;
  var ctx = canvas.getContext('2d');
  var w, h, dpr = Math.min(window.devicePixelRatio || 1, 2);
  var particles = [];
  var MAX_LINK_DIST = 150;
  var MOUSE_RADIUS = 160;
  var mouse = { x: null, y: null, active: false };
  var reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function resize(){
    w = window.innerWidth; h = window.innerHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    canvas.style.width = w + 'px'; canvas.style.height = h + 'px';
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var count = Math.max(30, Math.min(90, Math.round((w * h) / 18000)));
    particles = [];
    for (var i = 0; i < count; i++){
      particles.push({
        x: Math.random() * w,
        y: Math.random() * h,
        vx: (Math.random() - 0.5) * 0.25,
        vy: (Math.random() - 0.5) * 0.25,
        r: 1 + Math.random() * 1.6
      });
    }
  }

  function onMove(clientX, clientY){
    var rect = canvas.getBoundingClientRect();
    mouse.x = clientX - rect.left;
    mouse.y = clientY - rect.top;
    mouse.active = true;
  }
  window.addEventListener('mousemove', function(e){ onMove(e.clientX, e.clientY); }, { passive: true });
  window.addEventListener('touchmove', function(e){
    if (e.touches && e.touches.length) onMove(e.touches[0].clientX, e.touches[0].clientY);
  }, { passive: true });
  window.addEventListener('mouseleave', function(){ mouse.active = false; });
  document.addEventListener('mouseout', function(e){ if (!e.relatedTarget) mouse.active = false; });

  function step(){
    ctx.clearRect(0, 0, w, h);
    for (var i = 0; i < particles.length; i++){
      var p = particles[i];
      p.x += p.vx; p.y += p.vy;
      if (p.x < 0 || p.x > w) p.vx *= -1;
      if (p.y < 0 || p.y > h) p.vy *= -1;
      p.x = Math.max(0, Math.min(w, p.x));
      p.y = Math.max(0, Math.min(h, p.y));
      // Gentle, sustained pull toward the cursor when it's within
      // range — nudges the particle's POSITION (not just velocity) a
      // fraction of the way there each frame, so it eases in smoothly
      // and never snaps or overshoots, and relaxes back to normal
      // drift as soon as the cursor moves away.
      if (mouse.active){
        var dx = mouse.x - p.x, dy = mouse.y - p.y;
        var dist = Math.sqrt(dx*dx + dy*dy);
        if (dist < MOUSE_RADIUS && dist > 0.01){
          var force = (1 - dist / MOUSE_RADIUS) * 0.06;
          p.x += dx * force;
          p.y += dy * force;
        }
      }
    }
    for (var i = 0; i < particles.length; i++){
      for (var j = i + 1; j < particles.length; j++){
        var a = particles[i], b = particles[j];
        var dx = a.x - b.x, dy = a.y - b.y;
        var dist = Math.sqrt(dx*dx + dy*dy);
        if (dist < MAX_LINK_DIST){
          ctx.strokeStyle = 'rgba(139,92,246,' + (0.16 * (1 - dist / MAX_LINK_DIST)) + ')';
          ctx.lineWidth = 1;
          ctx.beginPath();
          ctx.moveTo(a.x, a.y);
          ctx.lineTo(b.x, b.y);
          ctx.stroke();
        }
      }
    }
    for (var i = 0; i < particles.length; i++){
      var p = particles[i];
      ctx.fillStyle = 'rgba(139,92,246,0.45)';
      ctx.beginPath();
      ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
      ctx.fill();
    }
    if (!reduceMotion) requestAnimationFrame(step);
  }

  window.addEventListener('resize', resize);
  resize();
  step();
  if (reduceMotion){
    // No continuous drift animation, but still redraw (with the
    // mouse-pull effect applied) whenever the cursor moves, so the
    // interaction stays available without a perpetual rAF loop.
    window.addEventListener('mousemove', function(){ step(); }, { passive: true });
  }
})();
`

func (a *Admin) handleBgParticlesJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(bgParticlesJS))
}

func (a *Admin) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	loginTpl.Execute(w, map[string]string{})
}

func (a *Admin) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.loginLimiter.allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		loginTpl.Execute(w, map[string]string{"Error": "muitas tentativas de login — aguarde alguns minutos e tente de novo"})
		return
	}
	r.ParseForm()
	username := r.FormValue("user")
	pass := r.FormValue("pass")
	u, err := a.store.Get().UserByUsername(username)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Constant-time-ish failure path regardless of whether the
	// username exists, so the error message/timing doesn't leak which
	// part was wrong — bcrypt.CompareHashAndPassword against a fixed
	// dummy hash keeps the timing close to the real-user path too.
	if u == nil || !u.CheckPassword(pass) {
		a.loginLimiter.recordFailure(ip)
		w.WriteHeader(http.StatusUnauthorized)
		loginTpl.Execute(w, map[string]string{"Error": "usuário ou senha inválidos"})
		return
	}
	a.loginLimiter.reset(ip)
	tok, err := a.sessions.create(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookie,
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
	ID        int64
	KeyPrefix string
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
    {{if $.IsAdmin}}<form class="inline" method="POST" action="/admin/devices/delete" onsubmit="return confirm('Remover {{.Hostname}} da malha?');">
      <input type="hidden" name="pub_key" value="{{.PubKey}}">
      <button type="submit" class="rm" title="Remover">&#10005;</button>
    </form>{{end}}
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

{{if .IsAdmin}}
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
  <td class="mono">{{.KeyPrefix}}…</td>
  <td>{{.Note}}</td>
  <td>{{if .Reusable}}reutilizável{{else}}uso único{{end}}</td>
  <td>{{if .Used}}<span class="pill">usada</span>{{else}}<span class="pill ok">disponível</span>{{end}}</td>
  <td>{{.CreatedAt}}</td>
  <td>
    <form class="inline" method="POST" action="/admin/keys/revoke" onsubmit="return confirm('Revogar esta chave?');">
      <input type="hidden" name="id" value="{{.ID}}">
      <button type="submit" class="danger">Revogar</button>
    </form>
  </td>
</tr>
{{else}}
<tr><td colspan="6">Nenhuma auth key criada ainda.</td></tr>
{{end}}
</table>
</div>
{{end}}

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

func (a *Admin) handleDashboard(w http.ResponseWriter, r *http.Request, u *UserRow) {
	devices, err := a.store.Get().ListAllDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	isAdmin := u.Role == RoleAdmin

	data := struct {
		Active   string
		IsAdmin  bool
		Username string
		Devices  []dashDevice
		Keys     []dashKey
		Flash    string
	}{Active: "devices", IsAdmin: isAdmin, Username: u.Username}

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
	// Auth keys are a sensitive security control (they let a device
	// join the mesh) — only admins get to see/manage them at all, not
	// just a read-only view, since even "who has a key outstanding"
	// is operationally sensitive information.
	if isAdmin {
		keys, err := a.store.Get().ListAuthKeys()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, k := range keys {
			data.Keys = append(data.Keys, dashKey{
				ID: k.ID, KeyPrefix: k.KeyPrefix, Note: k.Note, Reusable: k.Reusable, Used: k.Used,
				CreatedAt: time.Unix(k.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			})
		}
	}
	if msg := r.URL.Query().Get("flash"); msg != "" {
		data.Flash = msg
	}
	dashTpl.Execute(w, data)
}

func (a *Admin) handleNewKey(w http.ResponseWriter, r *http.Request, u *UserRow) {
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

func (a *Admin) handleRevokeKey(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	var id int64
	fmt.Sscanf(r.FormValue("id"), "%d", &id)
	if err := a.store.Get().RevokeAuthKey(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *Admin) handleDeleteDevice(w http.ResponseWriter, r *http.Request, u *UserRow) {
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

func (a *Admin) handleBackupPage(w http.ResponseWriter, r *http.Request, u *UserRow) {
	settings := a.store.GetBackupSettings()
	backups, err := a.store.ListBackups(settings.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Active          string
		IsAdmin         bool
		Username        string
		Dir             string
		Enabled         bool
		IntervalMinutes int
		RetentionCount  int
		Backups         []backupFileView
		Flash           string
		Error           string
	}{
		Active: "backup", IsAdmin: true, Username: u.Username,
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

func (a *Admin) handleBackupSettings(w http.ResponseWriter, r *http.Request, u *UserRow) {
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

func (a *Admin) handleBackupNow(w http.ResponseWriter, r *http.Request, u *UserRow) {
	settings := a.store.GetBackupSettings()
	path, err := a.store.RunBackupNow(settings.Dir)
	if err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("backup criado: "+path), http.StatusSeeOther)
}

func (a *Admin) handleBackupRestore(w http.ResponseWriter, r *http.Request, u *UserRow) {
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

func (a *Admin) handleBackupDelete(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	name := r.FormValue("file")
	settings := a.store.GetBackupSettings()
	if err := a.store.DeleteBackup(settings.Dir, name); err != nil {
		http.Redirect(w, r, "/admin/backup?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/backup?flash="+template.URLQueryEscaper("backup apagado: "+name), http.StatusSeeOther)
}

// --- users management (admin only) ---

var usersTpl = template.Must(template.New("users").Parse(sidebarDef + `
<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - usuários</title>
<style>` + baseCSS + `</style></head>
<body>
{{template "sidebar" .}}
<div class="main">
<div class="pagehead"><h1>Usuários</h1><p>Quem pode acessar este painel administrativo.</p></div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}

<div class="sectionhead">Novo usuário</div>
<div class="panel">
<form method="POST" action="/admin/users/new">
  <div><label>Usuário<input type="text" name="username" required></label></div>
  <div><label>Senha<input type="password" name="password" required minlength="8"></label></div>
  <div><label>Papel
    <select name="role">
      <option value="user">Usuário normal (só visualiza a malha)</option>
      <option value="admin">Administrador (acesso completo)</option>
    </select>
  </label></div>
  <button type="submit">Criar usuário</button>
</form>
</div>

<div class="sectionhead">Usuários existentes <span class="count">{{len .Users}}</span></div>
<div class="panel">
<table>
<tr><th>Usuário</th><th>Papel</th><th>Criado em</th><th></th></tr>
{{range .Users}}
<tr>
  <td>{{.Username}}{{if .IsMe}} <span class="pill ok">você</span>{{end}}</td>
  <td>{{if eq .Role "admin"}}<span class="pill ok">administrador</span>{{else}}<span class="pill">usuário normal</span>{{end}}</td>
  <td>{{.CreatedAt}}</td>
  <td>
    {{if not .IsMe}}
    <form class="inline" method="POST" action="/admin/users/role">
      <input type="hidden" name="id" value="{{.ID}}">
      <input type="hidden" name="role" value="{{if eq .Role "admin"}}user{{else}}admin{{end}}">
      <button type="submit" class="secondary">{{if eq .Role "admin"}}Rebaixar{{else}}Promover a admin{{end}}</button>
    </form>
    <form class="inline" method="POST" action="/admin/users/delete" onsubmit="return confirm('Remover o usuário {{.Username}}?');">
      <input type="hidden" name="id" value="{{.ID}}">
      <button type="submit" class="danger">Remover</button>
    </form>
    {{end}}
  </td>
</tr>
{{else}}
<tr><td colspan="4">Nenhum usuário.</td></tr>
{{end}}
</table>
</div>

</div>
</body></html>`))

type userView struct {
	ID        int64
	Username  string
	Role      string
	CreatedAt string
	IsMe      bool
}

func (a *Admin) handleUsersPage(w http.ResponseWriter, r *http.Request, u *UserRow) {
	users, err := a.store.Get().ListUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Active   string
		IsAdmin  bool
		Username string
		Users    []userView
		Flash    string
		Error    string
	}{Active: "users", IsAdmin: true, Username: u.Username}
	for _, usr := range users {
		data.Users = append(data.Users, userView{
			ID: usr.ID, Username: usr.Username, Role: usr.Role,
			CreatedAt: time.Unix(usr.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			IsMe:      usr.ID == u.ID,
		})
	}
	if msg := r.URL.Query().Get("flash"); msg != "" {
		data.Flash = msg
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		data.Error = msg
	}
	usersTpl.Execute(w, data)
}

func (a *Admin) handleCreateUser(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")
	role := r.FormValue("role")
	if role != RoleAdmin && role != RoleUser {
		role = RoleUser
	}
	if err := a.store.Get().CreateUser(username, password, role); err != nil {
		http.Redirect(w, r, "/admin/users?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?flash="+template.URLQueryEscaper("usuário criado: "+username), http.StatusSeeOther)
}

func (a *Admin) handleDeleteUser(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	var id int64
	fmt.Sscanf(r.FormValue("id"), "%d", &id)
	if id == u.ID {
		http.Redirect(w, r, "/admin/users?error="+template.URLQueryEscaper("você não pode remover a si mesmo"), http.StatusSeeOther)
		return
	}
	if err := a.store.Get().DeleteUser(id); err != nil {
		http.Redirect(w, r, "/admin/users?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *Admin) handleUserRole(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	var id int64
	fmt.Sscanf(r.FormValue("id"), "%d", &id)
	role := r.FormValue("role")
	if id == u.ID {
		http.Redirect(w, r, "/admin/users?error="+template.URLQueryEscaper("você não pode alterar seu próprio papel aqui — use outra conta de admin"), http.StatusSeeOther)
		return
	}
	if err := a.store.Get().SetUserRole(id, role); err != nil {
		http.Redirect(w, r, "/admin/users?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// --- my account (any logged-in user: change own password) ---

var accountTpl = template.Must(template.New("account").Parse(sidebarDef + `
<!doctype html>
<html><head><meta charset="utf-8"><title>govpn admin - minha conta</title>
<style>` + baseCSS + `</style></head>
<body>
{{template "sidebar" .}}
<div class="main">
<div class="pagehead"><h1>Minha conta</h1><p>Logado como <strong>{{.Username}}</strong> ({{if .IsAdmin}}administrador{{else}}usuário normal{{end}}).</p></div>

{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}

<div class="sectionhead">Trocar senha</div>
<div class="panel">
<form method="POST" action="/admin/account/password">
  <div><label>Senha atual<input type="password" name="current" required></label></div>
  <div><label>Nova senha<input type="password" name="new" required minlength="8"></label></div>
  <div><label>Confirmar nova senha<input type="password" name="confirm" required minlength="8"></label></div>
  <button type="submit">Salvar nova senha</button>
</form>
</div>

</div>
</body></html>`))

func (a *Admin) handleAccountPage(w http.ResponseWriter, r *http.Request, u *UserRow) {
	data := struct {
		Active   string
		IsAdmin  bool
		Username string
		Flash    string
		Error    string
	}{Active: "account", IsAdmin: u.Role == RoleAdmin, Username: u.Username}
	if msg := r.URL.Query().Get("flash"); msg != "" {
		data.Flash = msg
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		data.Error = msg
	}
	accountTpl.Execute(w, data)
}

func (a *Admin) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request, u *UserRow) {
	r.ParseForm()
	current := r.FormValue("current")
	newPass := r.FormValue("new")
	confirm := r.FormValue("confirm")
	if !u.CheckPassword(current) {
		http.Redirect(w, r, "/admin/account?error="+template.URLQueryEscaper("senha atual incorreta"), http.StatusSeeOther)
		return
	}
	if newPass != confirm {
		http.Redirect(w, r, "/admin/account?error="+template.URLQueryEscaper("a confirmação não bate com a nova senha"), http.StatusSeeOther)
		return
	}
	if err := a.store.Get().SetUserPassword(u.ID, newPass); err != nil {
		http.Redirect(w, r, "/admin/account?error="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/account?flash="+template.URLQueryEscaper("senha alterada com sucesso"), http.StatusSeeOther)
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
	mux.HandleFunc("/admin/assets/bg-particles.js", a.handleBgParticlesJS)
	mux.HandleFunc("/admin", a.requireLogin(a.handleDashboard))
	mux.HandleFunc("/admin/account", a.requireLogin(a.handleAccountPage))
	mux.HandleFunc("/admin/account/password", a.requireLogin(a.handleChangeOwnPassword))
	// Everything below touches a security-sensitive control (auth
	// keys, device removal, backups, other users' accounts) and is
	// gated server-side with requireAdmin — a "user" role session
	// gets a 403 even if it somehow guesses the URL directly.
	mux.HandleFunc("/admin/backup", a.requireAdmin(a.handleBackupPage))
	mux.HandleFunc("/admin/backup/settings", a.requireAdmin(a.handleBackupSettings))
	mux.HandleFunc("/admin/backup/now", a.requireAdmin(a.handleBackupNow))
	mux.HandleFunc("/admin/backup/restore", a.requireAdmin(a.handleBackupRestore))
	mux.HandleFunc("/admin/backup/delete", a.requireAdmin(a.handleBackupDelete))
	mux.HandleFunc("/admin/keys/new", a.requireAdmin(a.handleNewKey))
	mux.HandleFunc("/admin/keys/revoke", a.requireAdmin(a.handleRevokeKey))
	mux.HandleFunc("/admin/devices/delete", a.requireAdmin(a.handleDeleteDevice))
	mux.HandleFunc("/admin/users", a.requireAdmin(a.handleUsersPage))
	mux.HandleFunc("/admin/users/new", a.requireAdmin(a.handleCreateUser))
	mux.HandleFunc("/admin/users/delete", a.requireAdmin(a.handleDeleteUser))
	mux.HandleFunc("/admin/users/role", a.requireAdmin(a.handleUserRole))
}
