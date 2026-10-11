// Kimlik doğrulama: HMAC ile imzalı oturum çerezi, ortam değişkeni admini
// ve panelden yönetilen çoklu kullanıcı. Harici bağımlılık kullanmaz.
package panel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "niha_session"
	sessionTTL    = 7 * 24 * time.Hour
)

// User, panelden yönetilen kullanıcı hesabı. Şifre tuzlanıp hash'lenerek saklanır.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	PasswordSalt string `json:"passwordSalt"`
	PasswordEnc  string `json:"passwordEnc,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// ---------- Sırlar ve yardımcılar ----------

func sessionSecret() []byte {
	if s := os.Getenv("SESSION_SECRET"); s != "" {
		return []byte(s)
	}
	log.Printf("UYARI: SESSION_SECRET tanımlı değil; yerel geliştirme sırrı kullanılıyor")
	return []byte("niha-dev-secret-degistir")
}

// envAdmin, ortam değişkeninde tanımlı ana admini döndürür. Hiçbiri yoksa
// yerel geliştirme için admin/admin varsayılanını verir.
func envAdmin() (user, pass string, ok bool) {
	user = os.Getenv("ADMIN_USERNAME")
	pass = os.Getenv("ADMIN_PASSWORD")
	if user == "" && pass == "" {
		log.Printf("UYARI: ADMIN_USERNAME/ADMIN_PASSWORD tanımlı değil; admin/admin kullanılıyor")
		return "admin", "admin", true
	}
	return user, pass, user != "" && pass != ""
}

func newSalt() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPassword, tuz+şifreyi çok turlu SHA-256 ile hash'ler (basit KDF).
func hashPassword(pw, salt string) string {
	h := []byte(salt + "|" + pw)
	for i := 0; i < 100000; i++ {
		s := sha256.Sum256(h)
		h = s[:]
	}
	return hex.EncodeToString(h)
}

func checkPassword(pw, salt, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(hashPassword(pw, salt)), []byte(hash)) == 1
}

// passwordGCM, SESSION_SECRET'tan türetilen anahtarla AES-GCM şifreleyici döndürür.
func passwordGCM() cipher.AEAD {
	key := sha256.Sum256(append([]byte("niha-pw-enc|"), sessionSecret()...))
	block, _ := aes.NewCipher(key[:])
	gcm, _ := cipher.NewGCM(block)
	return gcm
}

// encryptPassword, şifreyi adminin görebilmesi için geri çözülebilir biçimde şifreler.
func encryptPassword(pw string) string {
	gcm := passwordGCM()
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(pw), nil))
}

// decryptPassword, şifreli şifreyi çözer; çözülemezse boş döner.
func decryptPassword(enc string) string {
	data, err := base64.StdEncoding.DecodeString(enc)
	gcm := passwordGCM()
	if enc == "" || err != nil || len(data) < gcm.NonceSize() {
		return ""
	}
	pw, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(pw)
}

// setPassword, kullanıcının hash'ini ve adminin göreceği şifreli kopyasını günceller.
func (u *User) setPassword(pw string) {
	u.PasswordSalt = newSalt()
	u.PasswordHash = hashPassword(pw, u.PasswordSalt)
	u.PasswordEnc = encryptPassword(pw)
}

// usernameTaken, adın ana adminde ya da (exceptID dışındaki) başka bir kullanıcıda olup olmadığını söyler.
func usernameTaken(name, exceptID string) bool {
	if isEnvAdmin(name) {
		return true
	}
	for _, u := range store.data.Users {
		if u.Username == name && u.ID != exceptID {
			return true
		}
	}
	return false
}

// ---------- Oturum jetonu ----------

func makeToken(username string) string {
	payload := fmt.Sprintf("%s|%d", username, time.Now().Add(sessionTTL).Unix())
	mac := hmac.New(sha256.New, sessionSecret())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyToken(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, sessionSecret())
	mac.Write(payload)
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return "", false
	}
	fields := strings.SplitN(string(payload), "|", 2)
	if len(fields) != 2 {
		return "", false
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return fields[0], true
}

func currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return verifyToken(c.Value)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// isEnvAdmin, verilen kullanıcının ortam değişkeninde tanımlı ana admin
// olup olmadığını söyler. Kullanıcı yönetimi yalnızca bu kişiye açıktır.
func isEnvAdmin(username string) bool {
	u, _, ok := envAdmin()
	return ok && username == u
}

// requireAuth, oturum gerektiren handler'ları sarar.
func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := currentUser(r); !ok {
			writeErr(w, http.StatusUnauthorized, "oturum gerekli")
			return
		}
		h(w, r)
	}
}

// requireAdmin, yalnızca ortam değişkenindeki ana admine izin verir.
func requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "oturum gerekli")
			return
		}
		if !isEnvAdmin(u) {
			writeErr(w, http.StatusForbidden, "bu işlem için yetkiniz yok")
			return
		}
		h(w, r)
	}
}

// ---------- Auth handler'ları ----------

func authenticate(username, password string) bool {
	if username == "" || password == "" {
		return false
	}
	if u, p, ok := envAdmin(); ok && username == u &&
		subtle.ConstantTimeCompare([]byte(password), []byte(p)) == 1 {
		return true
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()
	for _, u := range store.data.Users {
		if u.Username == username {
			return checkPassword(password, u.PasswordSalt, u.PasswordHash)
		}
	}
	return false
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "geçersiz veri")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if authenticate(body.Username, body.Password) {
		setSessionCookie(w, r, makeToken(body.Username))
		writeJSON(w, http.StatusOK, map[string]string{"username": body.Username})
		return
	}
	writeErr(w, http.StatusUnauthorized, "kullanıcı adı veya şifre hatalı")
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	if u, ok := currentUser(r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u, "admin": isEnvAdmin(u)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// ---------- Kullanıcı yönetimi ----------

func handleUsers(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	switch r.Method {
	case http.MethodGet:
		type userView struct {
			ID        string `json:"id"`
			Username  string `json:"username"`
			Password  string `json:"password"`
			CreatedAt string `json:"createdAt"`
		}
		list := []userView{}
		for _, u := range store.data.Users {
			list = append(list, userView{u.ID, u.Username, decryptPassword(u.PasswordEnc), u.CreatedAt})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		body.Username = strings.TrimSpace(body.Username)
		if body.Username == "" || body.Password == "" {
			writeErr(w, http.StatusBadRequest, "kullanıcı adı ve şifre zorunlu")
			return
		}
		if usernameTaken(body.Username, "") {
			writeErr(w, http.StatusBadRequest, "bu kullanıcı adı zaten var")
			return
		}
		u := User{ID: store.nextID("USR"), Username: body.Username, CreatedAt: now()}
		u.setPassword(body.Password)
		store.data.Users = append(store.data.Users, u)
		store.save()
		writeJSON(w, http.StatusCreated, map[string]string{"id": u.ID, "username": u.Username})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

func handleUserByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	idx := -1
	for i := range store.data.Users {
		if store.data.Users[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
		return
	}

	switch r.Method {
	case http.MethodPut:
		// Şifre boş gelirse mevcut şifre korunur.
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		body.Username = strings.TrimSpace(body.Username)
		if body.Username == "" {
			writeErr(w, http.StatusBadRequest, "kullanıcı adı zorunlu")
			return
		}
		if usernameTaken(body.Username, id) {
			writeErr(w, http.StatusBadRequest, "bu kullanıcı adı zaten var")
			return
		}
		u := &store.data.Users[idx]
		u.Username = body.Username
		if body.Password != "" {
			u.setPassword(body.Password)
		}
		store.save()
		writeJSON(w, http.StatusOK, map[string]string{"id": u.ID, "username": u.Username})

	case http.MethodDelete:
		store.data.Users = append(store.data.Users[:idx], store.data.Users[idx+1:]...)
		store.save()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}
