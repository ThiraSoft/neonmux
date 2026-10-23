// Spike jetable : vérifie sur l'iPhone que WebTransport avec empreinte
// épinglée, les passkeys et le cache hors ligne marchent dans une PWA.
// Rien ici n'est destiné à rester.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

//go:embed web
var webFS embed.FS

func main() {
	pageAddr := flag.String("page", "", "adresse HTTP de la page (ex. 127.0.0.1:8006), vide pour ne pas la servir")
	wtAddr := flag.String("wt", ":8007", "adresse UDP du serveur WebTransport")
	keyFile := flag.String("key", "spike-key.pem", "clé ECDSA du certificat (créée si absente)")
	flag.Parse()

	key := loadKey(*keyFile)
	ips := localIPs()
	der := makeCert(key, ips)
	sum := sha256.Sum256(der)
	hash := base64.StdEncoding.EncodeToString(sum[:])

	_, port, _ := net.SplitHostPort(*wtAddr)
	var urls []string
	for _, ip := range ips {
		if ip.IsLoopback() {
			continue
		}
		urls = append(urls, "https://"+net.JoinHostPort(ip.String(), port)+"/wt")
	}
	host, _ := os.Hostname()
	fmt.Printf("machine : %s\nempreinte (base64) : %s\nempreinte (hex) : %s\n", host, hash, hex.EncodeToString(sum[:]))
	for _, u := range urls {
		fmt.Println("url :", u)
	}

	if *pageAddr != "" {
		go servePage(*pageAddr, host, hash, urls)
	}

	h3 := &http3.Server{
		Addr:       *wtAddr,
		TLSConfig:  http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}),
		QUICConfig: &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	}
	webtransport.ConfigureHTTP3Server(h3)
	mux := http.NewServeMux()
	h3.Handler = mux
	wt := webtransport.Server{
		H3: h3,
		CheckOrigin: func(r *http.Request) bool {
			log.Printf("origine : %q depuis %s", r.Header.Get("Origin"), r.RemoteAddr)
			return true
		},
	}
	mux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrade : %v", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("session ouverte depuis %s", sess.RemoteAddr())
		go echo(sess, host)
	})
	log.Printf("WebTransport sur %s (udp)", *wtAddr)
	log.Fatal(wt.ListenAndServe())
}

// echo renvoie chaque flux bidirectionnel préfixé du nom de la machine.
func echo(sess *webtransport.Session, host string) {
	for {
		str, err := sess.AcceptStream(sess.Context())
		if err != nil {
			log.Printf("session fermée : %v", err)
			return
		}
		go func() {
			defer str.Close()
			b, err := io.ReadAll(io.LimitReader(str, 1<<16))
			if err != nil {
				log.Printf("lecture : %v", err)
				return
			}
			log.Printf("reçu : %q", b)
			fmt.Fprintf(str, "%s a reçu : %s", host, b)
		}()
	}
}

func servePage(addr, host, hash string, urls []string) {
	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": host, "hash": hash, "urls": urls})
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(icon())
	})
	log.Printf("page sur http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func loadKey(path string) *ecdsa.PrivateKey {
	if b, err := os.ReadFile(path); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				return k
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, _ := x509.MarshalECPrivateKey(k)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		log.Fatal(err)
	}
	return k
}

// makeCert produit un certificat de 13 jours aligné sur le lundi, pour que
// l'empreinte reste la même d'un lancement à l'autre pendant la semaine.
func makeCert(key *ecdsa.PrivateKey, ips []net.IP) []byte {
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from = from.AddDate(0, 0, -int((from.Weekday()+6)%7))
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(from.Unix()),
		Subject:               pkix.Name{CommonName: "neonmux-spike"},
		NotBefore:             from,
		NotAfter:              from.Add(13 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
	}
	// Signature déterministe (rand nil) : même certificat, même empreinte.
	der, err := x509.CreateCertificate(nil, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return der
}

func localIPs() []net.IP {
	var ips []net.IP
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-") || strings.HasPrefix(ifc.Name, "veth") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			if n.IP.IsLoopback() || n.IP.IsPrivate() || strings.HasPrefix(n.IP.String(), "100.") {
				ips = append(ips, n.IP)
			}
		}
	}
	return ips
}

func icon() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 180, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 180; x++ {
			c := color.RGBA{18, 10, 40, 255}
			if (x/20+y/20)%2 == 0 && x > 40 && x < 140 && y > 40 && y < 140 {
				c = color.RGBA{255, 40, 200, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
