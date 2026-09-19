// Package panel, 3D baskı panelinin tüm iş mantığını içerir.
// Hem yerel sunucu (main.go) hem de Vercel fonksiyonu (api/index.go) bunu kullanır.
package panel

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

const (
	dataFile = "data.json"
	redisKey = "niha-panel"
)

// ---------- Veri modelleri ----------

type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"` // satış fiyatı (elle girilir)
	Stock       int     `json:"stock"`
	Material    string  `json:"material"` // eski alan; geriye dönük uyumluluk
	Description string  `json:"description"`
	// Maliyet için malzeme seçimleri. Maliyet, güncel filament fiyatı ve
	// ayarlardan dinamik hesaplanır; burada seçimler saklanır.
	FilamentID   string  `json:"filamentId"`
	Grams        float64 `json:"grams"`      // kullanılan filament (gram)
	PrintHours   float64 `json:"printHours"` // baskı süresi (saat)
	UsePackaging bool    `json:"usePackaging"`
	UseSticker   bool    `json:"useSticker"`
	CreatedAt    string  `json:"createdAt"`
}

// Filament, üründe seçilebilen filament türü ve kg fiyatı.
type Filament struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	PricePerKg float64 `json:"pricePerKg"`
	CreatedAt  string  `json:"createdAt"`
}

// Settings, maliyet hesabında kullanılan global sabit kalemler.
type Settings struct {
	PackagingCost float64 `json:"packagingCost"` // ambalaj birim maliyeti (₺)
	StickerCost   float64 `json:"stickerCost"`   // sticker birim maliyeti (₺)
	HourlyCost    float64 `json:"hourlyCost"`    // saatlik baskı maliyeti (₺)
}

type OrderItem struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Qty       int     `json:"qty"`
}

type Order struct {
	ID        string      `json:"id"`
	Customer  string      `json:"customer"`
	Phone     string      `json:"phone"`
	Address   string      `json:"address"`
	Items     []OrderItem `json:"items"`
	Total     float64     `json:"total"`
	Status    string      `json:"status"` // yeni, baskida, kargoda, teslim, iptal
	Note      string      `json:"note"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

type storeData struct {
	Products  []Product  `json:"products"`
	Orders    []Order    `json:"orders"`
	Filaments []Filament `json:"filaments"`
	Settings  Settings   `json:"settings"`
	Users     []User     `json:"users"`
	Seq       int        `json:"seq"`
}

type Store struct {
	mu   sync.Mutex
	data storeData
}

var store = &Store{}

// ---------- Kalıcılık ----------
// Redis ortam değişkenleri varsa Upstash Redis (REST) kullanılır (Vercel),
// yoksa yerel data.json dosyası kullanılır.

func redisConfig() (url, token string, ok bool) {
	url = firstEnv("KV_REST_API_URL", "UPSTASH_REDIS_REST_URL")
	token = firstEnv("KV_REST_API_TOKEN", "UPSTASH_REDIS_REST_TOKEN")
	return url, token, url != "" && token != ""
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// redisCmd, Upstash REST API'sine bir komut gönderir ve "result" alanını döndürür.
func redisCmd(url, token string, args ...string) (json.RawMessage, error) {
	body, _ := json.Marshal(args)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("redis %d: %s", resp.StatusCode, string(raw))
	}
	var wrap struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	if wrap.Error != "" {
		return nil, fmt.Errorf("redis: %s", wrap.Error)
	}
	return wrap.Result, nil
}

// load, kalıcı depodan veriyi belleğe okur. Her istekte çağrılır ki
// birden çok serverless örneği arasında veri güncel kalsın.
func (s *Store) load() {
	if url, token, ok := redisConfig(); ok {
		res, err := redisCmd(url, token, "GET", redisKey)
		if err != nil {
			log.Printf("redis okuma hatası: %v", err)
			return
		}
		if len(res) == 0 || string(res) == "null" {
			s.data = storeData{} // henüz veri yok
			return
		}
		var payload string
		if err := json.Unmarshal(res, &payload); err != nil {
			log.Printf("redis çözümleme hatası: %v", err)
			return
		}
		var d storeData
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			log.Printf("veri çözümleme hatası: %v", err)
			return
		}
		s.data = d
		return
	}

	// Yerel dosya modu
	b, err := os.ReadFile(dataFile)
	if err != nil {
		s.data = storeData{}
		return
	}
	var d storeData
	if err := json.Unmarshal(b, &d); err != nil {
		log.Printf("veri okunamadı: %v", err)
		return
	}
	s.data = d
}

func (s *Store) save() {
	b, err := json.Marshal(s.data)
	if err != nil {
		log.Printf("veri serialize edilemedi: %v", err)
		return
	}
	if url, token, ok := redisConfig(); ok {
		if _, err := redisCmd(url, token, "SET", redisKey, string(b)); err != nil {
			log.Printf("redis yazma hatası: %v", err)
		}
		return
	}
	if err := os.WriteFile(dataFile, b, 0644); err != nil {
		log.Printf("veri yazılamadı: %v", err)
	}
}

func (s *Store) nextID(prefix string) string {
	s.data.Seq++
	return fmt.Sprintf("%s-%04d", prefix, s.data.Seq)
}

func now() string { return time.Now().Format("2006-01-02 15:04") }

// ---------- Yardımcılar ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ---------- Ürün handler'ları ----------

func handleProducts(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	switch r.Method {
	case http.MethodGet:
		list := append([]Product{}, store.data.Products...)
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var p Product
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		if p.Name == "" {
			writeErr(w, http.StatusBadRequest, "ürün adı zorunlu")
			return
		}
		p.ID = store.nextID("URN")
		p.CreatedAt = now()
		store.data.Products = append(store.data.Products, p)
		store.save()
		writeJSON(w, http.StatusCreated, p)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

func handleProductByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	idx := -1
	for i := range store.data.Products {
		if store.data.Products[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeErr(w, http.StatusNotFound, "ürün bulunamadı")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var p Product
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		p.ID = id
		p.CreatedAt = store.data.Products[idx].CreatedAt
		store.data.Products[idx] = p
		store.save()
		writeJSON(w, http.StatusOK, p)

	case http.MethodDelete:
		store.data.Products = append(store.data.Products[:idx], store.data.Products[idx+1:]...)
		store.save()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

// ---------- Filament handler'ları ----------

func handleFilaments(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	switch r.Method {
	case http.MethodGet:
		list := append([]Filament{}, store.data.Filaments...)
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var f Filament
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		if f.Name == "" {
			writeErr(w, http.StatusBadRequest, "filament adı zorunlu")
			return
		}
		f.ID = store.nextID("FIL")
		f.CreatedAt = now()
		store.data.Filaments = append(store.data.Filaments, f)
		store.save()
		writeJSON(w, http.StatusCreated, f)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

func handleFilamentByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	idx := -1
	for i := range store.data.Filaments {
		if store.data.Filaments[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeErr(w, http.StatusNotFound, "filament bulunamadı")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var f Filament
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		f.ID = id
		f.CreatedAt = store.data.Filaments[idx].CreatedAt
		store.data.Filaments[idx] = f
		store.save()
		writeJSON(w, http.StatusOK, f)

	case http.MethodDelete:
		store.data.Filaments = append(store.data.Filaments[:idx], store.data.Filaments[idx+1:]...)
		store.save()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

// ---------- Ayarlar handler'ı ----------

func handleSettings(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, store.data.Settings)

	case http.MethodPut:
		var s Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		store.data.Settings = s
		store.save()
		writeJSON(w, http.StatusOK, s)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

// ---------- Sipariş handler'ları ----------

var validStatus = map[string]bool{
	"yeni": true, "baskida": true, "kargoda": true, "teslim": true, "iptal": true,
}

func handleOrders(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	switch r.Method {
	case http.MethodGet:
		list := append([]Order{}, store.data.Orders...)
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var o Order
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		if o.Customer == "" {
			writeErr(w, http.StatusBadRequest, "müşteri adı zorunlu")
			return
		}
		if len(o.Items) == 0 {
			writeErr(w, http.StatusBadRequest, "en az bir ürün ekleyin")
			return
		}
		o.Total = 0
		for _, it := range o.Items {
			o.Total += it.Price * float64(it.Qty)
		}
		o.ID = store.nextID("SIP")
		o.Status = "yeni"
		o.CreatedAt = now()
		o.UpdatedAt = o.CreatedAt
		store.data.Orders = append(store.data.Orders, o)
		store.save()
		writeJSON(w, http.StatusCreated, o)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

func handleOrderByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	idx := -1
	for i := range store.data.Orders {
		if store.data.Orders[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeErr(w, http.StatusNotFound, "sipariş bulunamadı")
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var body struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz veri")
			return
		}
		if body.Status != "" {
			if !validStatus[body.Status] {
				writeErr(w, http.StatusBadRequest, "geçersiz durum")
				return
			}
			store.data.Orders[idx].Status = body.Status
		}
		if body.Note != "" {
			store.data.Orders[idx].Note = body.Note
		}
		store.data.Orders[idx].UpdatedAt = now()
		store.save()
		writeJSON(w, http.StatusOK, store.data.Orders[idx])

	case http.MethodDelete:
		store.data.Orders = append(store.data.Orders[:idx], store.data.Orders[idx+1:]...)
		store.save()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
	}
}

// ---------- Özet ----------

func handleStats(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	var revenue float64
	activeOrders := 0
	for _, o := range store.data.Orders {
		if o.Status == "teslim" {
			revenue += o.Total
		}
		if o.Status != "teslim" && o.Status != "iptal" {
			activeOrders++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"products":     len(store.data.Products),
		"orders":       len(store.data.Orders),
		"activeOrders": activeOrders,
		"revenue":      revenue,
	})
}

// ---------- Router ----------

// Mux, panelin tüm rotalarını içeren http.Handler döndürür.
func Mux() http.Handler {
	mux := http.NewServeMux()
	// Kimlik doğrulama (herkese açık)
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/me", handleMe)

	// Korunan uç noktalar (oturum gerekli)
	mux.HandleFunc("/api/products", requireAuth(handleProducts))
	mux.HandleFunc("/api/products/{id}", requireAuth(handleProductByID))
	mux.HandleFunc("/api/filaments", requireAuth(handleFilaments))
	mux.HandleFunc("/api/filaments/{id}", requireAuth(handleFilamentByID))
	mux.HandleFunc("/api/settings", requireAuth(handleSettings))
	mux.HandleFunc("/api/orders", requireAuth(handleOrders))
	mux.HandleFunc("/api/orders/{id}", requireAuth(handleOrderByID))
	mux.HandleFunc("/api/stats", requireAuth(handleStats))
	mux.HandleFunc("/api/users", requireAuth(handleUsers))
	mux.HandleFunc("/api/users/{id}", requireAuth(handleUserByID))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	return mux
}
