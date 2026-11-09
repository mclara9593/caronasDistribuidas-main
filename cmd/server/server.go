package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"net"
    "net/url"


	"github.com/mclara9593/caronasDistribuidas/internal/server"
)

func main() {
	id := flag.String("id", "", "ID desta empresa: A, B ou C")
	tcpAddr := flag.String("tcp", "", "Endereço TCP para clientes, por exemplo :9593")
	httpAddr := flag.String("http", "", "Endereço REST para os outros servidores, por exemplo :8081")
	peersArg := flag.String(
		"peers",
		"",
		"Servidores REST remotos no formato ID=IP:PORT, separados por vírgula; por exemplo B=192.168.1.25:8082,C=192.168.1.26:8083",
	)
	flag.Parse()

	if *id == "" || *tcpAddr == "" || *httpAddr == "" {
		log.Fatal("informe -id, -tcp e -http")
	}

	peers, err := parsePeers(*peersArg)
	if err != nil {
		log.Fatalf("configuração de peers inválida: %v", err)
	}

	if _, existe := peers[*id]; existe {
		log.Fatalf("o servidor não deve listar a si mesmo em -peers")
	}

	cfg := server.Config{
		IDServidor: *id,
		TCPAddr:    *tcpAddr,
		HTTPAddr:   *httpAddr,
		Peers:      peers,
	}

	srv := server.NovoServidor(cfg)

	log.Printf(
		"Iniciando empresa %s (TCP %s, REST %s), peers: %v",
		*id,
		*tcpAddr,
		*httpAddr,
		peers,
	)

	if err := srv.Iniciar(); err != nil {
		log.Fatalf("erro ao iniciar servidor %s: %v", *id, err)
	}
}

func parsePeers(valor string) (map[string]string, error) {
    peers := make(map[string]string)

    if strings.TrimSpace(valor) == "" {
        return peers, nil
    }

    for _, entrada := range strings.Split(valor, ",") {
        partes := strings.SplitN(strings.TrimSpace(entrada), "=", 2)
        if len(partes) != 2 {
            return nil, fmt.Errorf(
                "entrada %q: esperado ID=IP:PORT",
                entrada,
            )
        }

        id := strings.TrimSpace(partes[0])
        endereco := strings.TrimSpace(partes[1])

        if id == "" || endereco == "" {
            return nil, fmt.Errorf("entrada incompleta: %q", entrada)
        }

        if _, repetido := peers[id]; repetido {
            return nil, fmt.Errorf("ID de servidor repetido: %s", id)
        }

        // Aceita tanto IP:PORT quanto uma URL completa.
        if !strings.Contains(endereco, "://") {
            endereco = "http://" + endereco
        }

        u, err := url.Parse(endereco)
        if err != nil || u.Hostname() == "" {
            return nil, fmt.Errorf("endereço inválido para o servidor %s: %q", id, endereco)
        }

        if _, _, err := net.SplitHostPort(u.Host); err != nil {
            return nil, fmt.Errorf(
                "endereço do servidor %s deve incluir IP e porta, por exemplo 192.168.1.25:8082",
                id,
            )
        }

        peers[id] = strings.TrimRight(endereco, "/")
    }

    return peers, nil
}