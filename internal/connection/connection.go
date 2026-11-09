package connection

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	protocol "github.com/mclara9593/caronasDistribuidas/internal/protocol"
)



// EscolherServidor pergunta ao usuário a qual empresa do consórcio ele
// deseja se conectar e opcionalmente permite informar um endereço customizado.
// ServidoresConsorcio mapeia cada empresa ao endereço TCP informado
// pelo usuário durante a execução.
var ServidoresConsorcio = map[string]string{
    "A": "",
    "B": "",
    "C": "",
}

// EscolherServidor pergunta a qual empresa o usuário deseja se conectar
// e solicita o IP e a porta do servidor dessa empresa.
func EscolherServidor(reader *bufio.Reader) (idEmpresa string, endereco string, err error) {
    fmt.Println("\nA qual empresa do consórcio você deseja se conectar?")
    fmt.Println("  A")
    fmt.Println("  B")
    fmt.Println("  C")
    fmt.Print("Digite a letra da empresa (A, B ou C): ")

    escolha, err := reader.ReadString('\n')
    if err != nil {
        return "", "", fmt.Errorf("erro ao ler a empresa: %w", err)
    }
    escolha = strings.ToUpper(strings.TrimSpace(escolha))

    if _, existe := ServidoresConsorcio[escolha]; !existe {
        return "", "", fmt.Errorf("empresa %q não reconhecida", escolha)
    }

    fmt.Printf("Digite o IP e a porta do servidor da empresa %s (ex.: 192.168.1.25:9593): ", escolha)

    endereco, err = reader.ReadString('\n')
    if err != nil {
        return "", "", fmt.Errorf("erro ao ler o endereço do servidor: %w", err)
    }
    endereco = strings.TrimSpace(endereco)

    if endereco == "" {
        return "", "", fmt.Errorf("o endereço do servidor não pode ficar vazio")
    }

    return escolha, endereco, nil
}

// função para conectar ao servidor com tentativas limitadas
func ConectarAoServidor(endereco string, maxTentativas int) (net.Conn, error) {
	var conn net.Conn
	var err error

	for i := 1; i <= maxTentativas; i++ {
		fmt.Printf(" Tentando conectar ao servidor (%d/%d)...\n", i, maxTentativas)

		// net.DialTimeout evita que o cliente fique travado indefinidamente
		// se a rede falhar. A tentativa acontece sequencialmente, dentro da
		// mesma goroutine que chamou esta função — não há goroutine
		// concorrente tentando outro endereço ao mesmo tempo.
		conn, err = net.DialTimeout("tcp", endereco, 3*time.Second)
		if err == nil {
			return conn, nil // Conexão estabelecida com sucesso!
		}

		fmt.Printf("Falha na conexão: %v. Tentando novamente em 2 segundos...\n", err)
		time.Sleep(2 * time.Second)
	}

	return nil, err // Retorna o último erro se esgotar as tentativas
}

type Cliente struct {
	Nome          string
	IDEmpresa     string // identifica a empresa do consórcio a quem este cliente está conectado
	Conn          net.Conn
	Reader        *bufio.Reader
	IsLogged      bool
	UsuarioLogado string // email de quem está logado
	NomeLogado    string // nome de quem está logado (vem do servidor no login)
}

func NewCliente(nome string, idEmpresa string, conn net.Conn) *Cliente {
	return &Cliente{
		Nome:      nome,
		IDEmpresa: idEmpresa,
		Conn:      conn,
		Reader:    bufio.NewReader(conn),
		IsLogged:  false,
	}
}

// EnviarRequisicao envia qualquer ação/payload e retorna a resposta do
// servidor. Toda a operação — serializar, escrever no socket e aguardar
// a leitura da resposta — acontece de forma bloqueante, dentro da mesma
// goroutine que a chamou. Não é aberta nenhuma goroutine nova para
// enviar ou para ler a resposta: o cliente só avança para a próxima
// linha de código depois que a resposta completa chegar (ou der erro).
func (c *Cliente) EnviarRequisicao(action string, payload interface{}) (*protocol.Response, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("erro ao serializar payload: %v", err)
	}

	req := protocol.Request{
		Action:    action,
		RequestID: fmt.Sprintf("req-%d", time.Now().UnixNano()),
		Payload:   payloadBytes,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("erro ao serializar requisição: %v", err)
	}

	_, err = c.Conn.Write(append(reqBytes, '\n'))
	if err != nil {
		return nil, fmt.Errorf("erro ao enviar dados pelo socket: %v", err)
	}

	respostaBytes, err := c.Reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta do servidor: %v", err)
	}

	var resp protocol.Response
	err = json.Unmarshal(respostaBytes, &resp)
	if err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta JSON: %v", err)
	}

	return &resp, nil
}
