// pkg/transport/quic.go
package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// Listen démarre un listener QUIC sur l'adresse donnée
func Listen(addr string) (*quic.Listener, error) {
	tlsConf, err := getTLSConfig(true)
	if err != nil {
		log.Printf("ERROR: Échec de la génération de la config TLS: %v\n", err)
		var emptyListener *quic.Listener
		return emptyListener, err // Ensure the return type matches the function signature
	}

	// Configurer le listener QUIC
	// Pour l'instant, utilisons une config QUIC par défaut.
	// Plus tard, nous ajouterons les paramètres MP-QUIC, etc.
	quicConf := &quic.Config{
		// MaxIdleTimeout: 30 * time.Second, // Exemple
		// KeepAlivePeriod: 15 * time.Second, // Exemple
	}

	listener, err := quic.ListenAddr(addr, tlsConf, quicConf)
	if err != nil {
		log.Printf("ERROR: Échec de l'écoute QUIC sur %s: %v\n", addr, err)
		var emptyListener *quic.Listener
		return emptyListener, err
	}
	return listener, nil
}

// TODO: Implémenter les fonctions pour Dial (connexion sortante)
func Dial(ctx context.Context, addr string) (quic.Connection, error) {
	tlsConf, err := getTLSConfig(false) // false pour client
	if err != nil {
		log.Printf("ERROR: Échec de la génération de la config TLS client: %v\n", err)
		return nil, err
	}

	// Configurer QUIC (peut être ajusté)
	quicConf := &quic.Config{}

	udpConn, err := net.ListenUDP("udp", nil) // Laisse le système choisir un port source
	if err != nil {
		return nil, err
	}
	// Ne pas oublier de fermer udpConn si DialAddr échoue ou quand la connexion QUIC est fermée
	// quic-go le gère généralement, mais il faut être prudent.

	remoteAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		udpConn.Close()
		return nil, err
	}

	conn, err := quic.Dial(ctx, udpConn, remoteAddr, tlsConf, quicConf)
	if err != nil {
		udpConn.Close() // Important de fermer le socket UDP si Dial échoue
		log.Printf("TRANSPORT: Échec de la connexion QUIC à %s: %v\n", addr, err)
		return nil, err
	}

	// Le socket UDP est maintenant géré par la connexion QUIC, ne pas le fermer ici.
	log.Printf("TRANSPORT: Connexion QUIC établie avec %s (depuis %s)\n", conn.RemoteAddr(), conn.LocalAddr())
	return conn, nil
}

var (
	cachedClientTLS *tls.Config
	cachedServerTLS *tls.Config
	tlsGenMutex     sync.Mutex
)

// getTLSConfig crée une configuration TLS auto-signée pour QUIC (pour test)
func getTLSConfig(isServer bool) (*tls.Config, error) {
	tlsGenMutex.Lock()
	defer tlsGenMutex.Unlock()

	if isServer && cachedServerTLS != nil {
		return cachedServerTLS, nil
	}
	if !isServer && cachedClientTLS != nil {
		return cachedClientTLS, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	config := &tls.Config{
		Certificates:       []tls.Certificate{tlsCert},
		NextProtos:         []string{"dqmp-1.0"},
		InsecureSkipVerify: !isServer, // Accepter auto-signé pour client test
	}

	if isServer {
		cachedServerTLS = config
	} else {
		cachedClientTLS = config
	}
	return config, nil
}

// TODO: Gérer les frames custom (probablement via des streams dédiés)
// TODO: Intégrer la configuration MP-QUIC
