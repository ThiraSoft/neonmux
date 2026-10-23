// Client de vérification locale du spike : connexion avec empreinte épinglée.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

func main() {
	url, hash := os.Args[1], os.Args[2]
	tc := &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error {
		h := sha256.Sum256(s.PeerCertificates[0].Raw)
		if base64.StdEncoding.EncodeToString(h[:]) != hash {
			return fmt.Errorf("empreinte différente")
		}
		return nil
	}}
	cl := &webtransport.Transport{TLSClientConfig: tc, QUICConfig: &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, sess, err := cl.Dial(ctx, url, nil)
	if err != nil {
		fmt.Println("échec :", err)
		os.Exit(1)
	}
	s, _ := sess.OpenStreamSync(ctx)
	s.Write([]byte("ping"))
	s.Close()
	b, _ := io.ReadAll(s)
	fmt.Println(string(b))
}
