# 3D Baskı Paneli

3D baskı ürünleriniz için minimalist e-ticaret yönetim paneli. Sadece Go — harici kütüphane yok.

## Yerelde çalıştırma

```bash
go run .
```

Tarayıcıdan aç: **http://localhost:8080** (port değiştirmek için: `PORT=9000 go run .`)

Yerelde veriler proje klasöründeki `data.json` dosyasında tutulur.

## Özellikler

- **Ürünler** — ekle / düzenle / sil, fiyat, stok, materyal (PLA, PETG, reçine…)
- **Siparişler** — çok ürünlü sipariş, müşteri adı, telefon, **adres**, otomatik toplam
- **Durum takibi** — Yeni → Baskıda → Kargoda → Teslim (ve İptal)
- **Özet** — aktif sipariş, toplam sipariş, ürün çeşidi, teslim edilen ciro

## Vercel'e yayınlama

Vercel serverless çalışır ve diske kalıcı yazamaz. Bu yüzden yayında veriler
**Upstash Redis** üzerinde saklanır. Adımlar:

1. Projeyi bir Git deposuna (GitHub/GitLab) yükleyin.
2. [vercel.com](https://vercel.com) → **Add New → Project** → deponuzu içe aktarın.
   Vercel Go projesini otomatik tanır (`api/` klasörü + `vercel.json`).
3. **Storage** sekmesinden bir **Upstash Redis (KV)** veritabanı ekleyip projeye
   bağlayın. Bu, aşağıdaki ortam değişkenlerini otomatik ekler:
   - `KV_REST_API_URL`
   - `KV_REST_API_TOKEN`
   
   (Bu değişkenler varsa uygulama otomatik olarak Redis'i kullanır; yoksa dosya modu.)
4. **Deploy** deyin. Bitince verilen `*.vercel.app` adresinden panele erişebilirsiniz.

### CLI ile (alternatif)

```bash
npm i -g vercel
vercel            # projeyi bağla ve deploy et
vercel env add KV_REST_API_URL     # Upstash'ten aldığınız değerleri girin
vercel env add KV_REST_API_TOKEN
vercel --prod
```

## Proje yapısı

```
main.go            Yerel geliştirme sunucusu
api/index.go       Vercel serverless giriş noktası
panel/panel.go     Tüm iş mantığı (model, API, depolama)
panel/web/         Arayüz (index.html)
vercel.json        Tüm istekleri Go fonksiyonuna yönlendirir
```
