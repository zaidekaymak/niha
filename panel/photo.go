// Ürün fotoğrafları: ana veriyi şişirmemek için her fotoğraf ayrı anahtarda
// (Redis) ya da yerelde photos/ klasöründe data URL olarak saklanır.
package panel

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	photoDir      = "photos"
	maxPhotoBytes = 1 << 20 // çözülmüş fotoğraf için üst sınır (1 MB)
)

var photoTypes = []string{"image/jpeg", "image/png", "image/webp"}

func photoKey(id string) string { return redisKey + ":photo:" + id }

// photoPath, yerel modda fotoğraf dosyasının yolunu döndürür.
func photoPath(id string) string { return filepath.Join(photoDir, filepath.Base(id)) }

func photoGet(id string) (string, bool) {
	if url, token, ok := redisConfig(); ok {
		res, err := redisCmd(url, token, "GET", photoKey(id))
		if err != nil || len(res) == 0 || string(res) == "null" {
			return "", false
		}
		var s string
		if json.Unmarshal(res, &s) != nil {
			return "", false
		}
		return s, true
	}
	b, err := os.ReadFile(photoPath(id))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func photoSet(id, dataURL string) error {
	if url, token, ok := redisConfig(); ok {
		_, err := redisCmd(url, token, "SET", photoKey(id), dataURL)
		return err
	}
	if err := os.MkdirAll(photoDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(photoPath(id), []byte(dataURL), 0644)
}

func photoDel(id string) {
	if url, token, ok := redisConfig(); ok {
		if _, err := redisCmd(url, token, "DEL", photoKey(id)); err != nil {
			log.Printf("fotoğraf silinemedi: %v", err)
		}
		return
	}
	os.Remove(photoPath(id))
}

// parsePhoto, data URL'i doğrular ve içerik türü ile ham baytları döndürür.
func parsePhoto(dataURL string) (string, []byte, bool) {
	meta, payload, found := strings.Cut(dataURL, ",")
	if !found || !strings.HasPrefix(meta, "data:") || !strings.HasSuffix(meta, ";base64") {
		return "", nil, false
	}
	ctype := strings.TrimSuffix(strings.TrimPrefix(meta, "data:"), ";base64")
	allowed := false
	for _, t := range photoTypes {
		allowed = allowed || ctype == t
	}
	if !allowed {
		return "", nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) == 0 || len(raw) > maxPhotoBytes {
		return "", nil, false
	}
	return ctype, raw, true
}

// handleProductPhoto, ürün fotoğrafını sunar (GET), yükler (PUT) veya siler (DELETE).
func handleProductPhoto(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if r.Method == http.MethodGet {
		dataURL, ok := photoGet(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctype, raw, ok := parsePhoto(dataURL)
		if !ok {
			http.NotFound(w, r)
			return
		}
		// URL ?v=<sürüm> taşıdığı için uzun süre önbelleğe alınabilir.
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Write(raw)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "desteklenmeyen metot")
		return
	}

	// Gövde doğrulaması depo okunmadan önce yapılır.
	var dataURL string
	if r.Method == http.MethodPut {
		var body struct {
			Photo string `json:"photo"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxPhotoBytes*2)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "fotoğraf okunamadı (en fazla 1 MB)")
			return
		}
		if _, _, ok := parsePhoto(body.Photo); !ok {
			writeErr(w, http.StatusBadRequest, "geçersiz fotoğraf (JPEG/PNG/WebP, en fazla 1 MB)")
			return
		}
		dataURL = body.Photo
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.load()

	var p *Product
	for i := range store.data.Products {
		if store.data.Products[i].ID == id {
			p = &store.data.Products[i]
			break
		}
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "ürün bulunamadı")
		return
	}

	if r.Method == http.MethodPut {
		if err := photoSet(id, dataURL); err != nil {
			log.Printf("fotoğraf kaydedilemedi: %v", err)
			writeErr(w, http.StatusInternalServerError, "fotoğraf kaydedilemedi")
			return
		}
		p.PhotoVer = strconv.FormatInt(time.Now().UnixMilli(), 36)
	} else {
		photoDel(id)
		p.PhotoVer = ""
	}
	store.save()
	writeJSON(w, http.StatusOK, p)
}
