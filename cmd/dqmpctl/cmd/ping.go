// cmd/dqmpctl/cmd/ping.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	// On a besoin d'une fonction Dial et d'une manière d'ouvrir un stream et d'envoyer/recevoir.
	// Pour éviter de dupliquer la logique de dial+ping dans dqmpctl, on pourrait :
	// 1. Exposer une fonction Ping client dans un package partagé.
	// 2. Ou (plus simple pour l'instant) réimplémenter la logique client PING ici.
	// Choisissons l'option 2 pour ce premier jet.
	"bufio"
	"strings"

	"github.com/bradanzie/dqmp/pkg/transport" // Adaptez
)

const (
	cliPingTimeout   = 10 * time.Second
	pingStreamMsgCLI = "PING"
	pongStreamMsgCLI = "PONG"
)

// pingCmd represents the ping command
var pingCmd = &cobra.Command{
	Use:   "ping <node_address>",
	Short: "Envoie un PING à un nœud DQMP et attend un PONG",
	Long: `Vérifie la connectivité et la latence de base avec un nœud DQMP
spécifié par son adresse (ex: localhost:4242 ou 1.2.3.4:4242).`,
	Args: cobra.ExactArgs(1), // S'assure qu'on a exactement une adresse
	Run: func(cmd *cobra.Command, args []string) {
		targetAddr := args[0]
		fmt.Printf("Pinging %s...\n", targetAddr)

		// Créer un contexte avec timeout
		ctx, cancel := context.WithTimeout(context.Background(), cliPingTimeout)
		defer cancel()

		startTime := time.Now()

		// Établir la connexion QUIC
		conn, err := transport.Dial(ctx, targetAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur de connexion à %s: %v\n", targetAddr, err)
			os.Exit(1)
		}
		defer conn.CloseWithError(0, "CLI Ping done") // Fermer proprement

		connectDuration := time.Since(startTime)
		fmt.Printf("Connecté à %s en %v\n", targetAddr, connectDuration)

		// Ouvrir un stream
		streamCtx, streamCancel := context.WithTimeout(ctx, cliPingTimeout-connectDuration) // Utiliser le temps restant
		defer streamCancel()
		stream, err := conn.OpenStreamSync(streamCtx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur d'ouverture du stream vers %s: %v\n", targetAddr, err)
			os.Exit(1)
		}
		defer stream.Close()

		// Envoyer PING
		_, err = stream.Write([]byte(pingStreamMsgCLI + "\n"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur d'envoi PING vers %s: %v\n", targetAddr, err)
			os.Exit(1)
		}

		// Attendre PONG
		reader := bufio.NewReader(stream)
		stream.SetReadDeadline(time.Now().Add(cliPingTimeout - connectDuration)) // Définir deadline de lecture

		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur de réception PONG de %s: %v\n", targetAddr, err)
			os.Exit(1)
		}

		rtt := time.Since(startTime) // Temps total depuis le début
		response = strings.TrimSpace(response)

		if response == pongStreamMsgCLI {
			fmt.Printf("Réponse PONG reçue de %s en %v\n", targetAddr, rtt)
		} else {
			fmt.Fprintf(os.Stderr, "Réponse inattendue de %s: '%s'\n", targetAddr, response)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(pingCmd)

	// Flags locaux pour la commande ping (si nécessaire)
	// pingCmd.Flags().IntP("count", "c", 4, "Nombre de pings à envoyer")
}
