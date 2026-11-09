package consorcio

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mclara9593/caronasDistribuidas/internal/graph"
	"github.com/mclara9593/caronasDistribuidas/internal/protocol"
)

type ItemItinerario struct {
	SectionID string
	Assentos  int
}

func gerarTransacaoID(idServidor string) string {
	return fmt.Sprintf("res-%s-%d", idServidor, time.Now().UnixNano())
}

// resultadoEnvio é a resposta (ou erro) de um participante numa fase do 2PC.
type resultadoEnvio struct {
	servidor string
	resp     protocol.TransactionResponse
	err      error
}

// executarFase envia a mesma fase do 2PC a todos os servidores envolvidos ao
// mesmo tempo (ex.: A envia prepare para B e C em paralelo) e espera todas as
// respostas. O resultado mantém a ordem de `servidores`.
func executarFase(
	servidores []string,
	enviar func(servidor string) (protocol.TransactionResponse, error),
) []resultadoEnvio {
	resultados := make([]resultadoEnvio, len(servidores))

	var wg sync.WaitGroup
	for i, servidor := range servidores {
		wg.Add(1)
		go func(i int, servidor string) {
			defer wg.Done()
			resp, err := enviar(servidor)
			resultados[i] = resultadoEnvio{servidor: servidor, resp: resp, err: err}
		}(i, servidor)
	}
	wg.Wait()

	return resultados
}

// primeiraFalha devolve o primeiro participante (na ordem de envio) que deu
// erro ou recusou a fase, ou nil se todos aceitaram.
func primeiraFalha(resultados []resultadoEnvio) *resultadoEnvio {
	for i := range resultados {
		if resultados[i].err != nil || !resultados[i].resp.Accepted {
			return &resultados[i]
		}
	}
	return nil
}

// agruparPorServidor separa os trechos do itinerário por servidor dono,
// preservando a ordem em que cada servidor aparece pela primeira vez.
func agruparPorServidor(
	itens []ItemItinerario,
	exigirUmAssento bool,
	msgRepetido string,
) (map[string][]protocol.TransactionSection, []string, error) {
	porServidor := make(map[string][]protocol.TransactionSection)
	var ordem []string
	vistos := make(map[string]bool)

	for _, item := range itens {
		idLocal, servidor, err := protocol.DecomporSectionID(item.SectionID)
		if err != nil {
			return nil, nil, err
		}
		if idLocal == "" || servidor == "" {
			return nil, nil, fmt.Errorf("ID de trecho incompleto: %q", item.SectionID)
		}
		if exigirUmAssento && item.Assentos != 1 {
			return nil, nil, fmt.Errorf("a reserva deve ocupar um assento por trecho")
		}
		if vistos[item.SectionID] {
			return nil, nil, fmt.Errorf("%s: %s", msgRepetido, item.SectionID)
		}
		vistos[item.SectionID] = true

		if _, existe := porServidor[servidor]; !existe {
			ordem = append(ordem, servidor)
		}
		porServidor[servidor] = append(
			porServidor[servidor],
			protocol.TransactionSection{SectionID: item.SectionID, Seats: 1},
		)
	}

	return porServidor, ordem, nil
}

func CoordenarReserva(
	itinerario []ItemItinerario,
	passageiroEmail string,
	meuIDServidor string,
	relogio *RelogioLogico,
	grafo *graph.Grafo,
	peers *PeersRegistry,
) (string, error) {
	if len(itinerario) == 0 {
		return "", fmt.Errorf("itinerário sem trechos")
	}
	if passageiroEmail == "" || meuIDServidor == "" {
		return "", fmt.Errorf("email do passageiro e ID do servidor são obrigatórios")
	}
	if relogio == nil || grafo == nil || peers == nil {
		return "", fmt.Errorf("coordenador não inicializado")
	}

	porServidor, ordem, err := agruparPorServidor(itinerario, true, "trecho repetido no itinerário")
	if err != nil {
		return "", err
	}

	txID := gerarTransacaoID(meuIDServidor)
	timestamp := relogio.Tick()

	enviar := func(acao string) func(string) (protocol.TransactionResponse, error) {
		return func(servidor string) (protocol.TransactionResponse, error) {
			req := protocol.TransactionRequest{
				TransactionID:    txID,
				PassengerEmail:   passageiroEmail,
				OriginServer:     meuIDServidor,
				LamportTimestamp: timestamp,
				Sections:         porServidor[servidor],
			}
			return enviarTransacao(servidor, acao, req, meuIDServidor, relogio, grafo, peers)
		}
	}

	// Fase 1: prepare em todos os servidores ao mesmo tempo.
	if falha := primeiraFalha(executarFase(ordem, enviar("prepare"))); falha != nil {
		// Aborta em todos, inclusive no que falhou: se o prepare chegou mas a
		// resposta se perdeu, o abort (idempotente) limpa a pendência.
		executarFase(ordem, enviar("abort"))

		if falha.err != nil {
			return "", fmt.Errorf("prepare no servidor %s: %w", falha.servidor, falha.err)
		}
		return "", fmt.Errorf("prepare recusado por %s: %s", falha.servidor, falha.resp.Reason)
	}

	// Fase 2: todos aceitaram, então não se aborta parcialmente. O commit é
	// enviado a todos, mesmo que algum falhe, para não deixar os demais
	// pendentes.
	if falha := primeiraFalha(executarFase(ordem, enviar("commit"))); falha != nil {
		if falha.err != nil {
			return txID, fmt.Errorf("reserva %s pendente: commit em %s: %w", txID, falha.servidor, falha.err)
		}
		return txID, fmt.Errorf("commit recusado por %s: %s", falha.servidor, falha.resp.Reason)
	}

	return txID, nil
}

func CoordenarCancelamento(
	reservationID string,
	passageiroEmail string,
	trechos []ItemItinerario,
	meuIDServidor string,
	relogio *RelogioLogico,
	grafo *graph.Grafo,
	peers *PeersRegistry,
) error {
	if reservationID == "" || passageiroEmail == "" || meuIDServidor == "" {
		return fmt.Errorf("ID da reserva, email do passageiro e servidor são obrigatórios")
	}
	if len(trechos) == 0 {
		return fmt.Errorf("reserva sem trechos")
	}
	if relogio == nil || grafo == nil || peers == nil {
		return fmt.Errorf("coordenador não inicializado")
	}

	porServidor, ordem, err := agruparPorServidor(trechos, false, "trecho duplicado")
	if err != nil {
		return err
	}

	timestamp := relogio.Tick()

	enviar := func(acao string) func(string) (protocol.TransactionResponse, error) {
		return func(servidor string) (protocol.TransactionResponse, error) {
			req := protocol.TransactionRequest{
				TransactionID:    reservationID,
				PassengerEmail:   passageiroEmail,
				OriginServer:     meuIDServidor,
				LamportTimestamp: timestamp,
				Sections:         porServidor[servidor],
			}
			return enviarTransacao(servidor, acao, req, meuIDServidor, relogio, grafo, peers)
		}
	}

	if falha := primeiraFalha(executarFase(ordem, enviar("prepare-cancel"))); falha != nil {
		executarFase(ordem, enviar("abort-cancel"))

		if falha.err != nil {
			return fmt.Errorf("prepare de cancelamento em %s: %w", falha.servidor, falha.err)
		}
		return fmt.Errorf("prepare de cancelamento recusado por %s: %s", falha.servidor, falha.resp.Reason)
	}

	// Após todos os prepares, confirma em todos os participantes. Uma falha
	// aqui é reportada para evitar remover a carona parcialmente.
	if falha := primeiraFalha(executarFase(ordem, enviar("commit-cancel"))); falha != nil {
		if falha.err != nil {
			return fmt.Errorf("cancelamento %s pode estar pendente em %s: %w", reservationID, falha.servidor, falha.err)
		}
		return fmt.Errorf("commit de cancelamento recusado por %s: %s", falha.servidor, falha.resp.Reason)
	}

	return nil
}

// tentativasPorAcao: só o prepare falha "rápido" (quem decide é o
// coordenador, que aborta). As demais mensagens já são decisões tomadas e são
// idempotentes no participante, então vale repetir em caso de falha de rede.
func tentativasPorAcao(acao string) int {
	switch acao {
	case "prepare", "prepare-cancel":
		return 1
	default:
		return 3
	}
}

// enviarTransacao entrega uma mensagem do 2PC (reserva ou cancelamento) ao
// servidor dono dos trechos: direto, se for este mesmo servidor, ou por REST.
func enviarTransacao(
	servidor, acao string,
	req protocol.TransactionRequest,
	meuID string,
	relogio *RelogioLogico,
	grafo *graph.Grafo,
	peers *PeersRegistry,
) (protocol.TransactionResponse, error) {
	if servidor == meuID {
		switch acao {
		case "prepare":
			return TratarPrepare(grafo, relogio, meuID, req), nil
		case "commit":
			return TratarCommit(grafo, relogio, meuID, req), nil
		case "abort":
			return TratarAbort(grafo, relogio, meuID, req), nil
		case "prepare-cancel":
			return TratarPrepareCancelamento(grafo, relogio, meuID, req), nil
		case "commit-cancel":
			return TratarCommitCancelamento(grafo, relogio, meuID, req), nil
		case "abort-cancel":
			return TratarAbortCancelamento(grafo, relogio, meuID, req), nil
		default:
			return protocol.TransactionResponse{}, fmt.Errorf("ação desconhecida: %s", acao)
		}
	}

	urlBase, existe := peers.URLDoServidor(servidor)
	if !existe {
		return protocol.TransactionResponse{}, fmt.Errorf("servidor %s desconhecido", servidor)
	}

	var (
		resposta protocol.TransactionResponse
		err      error
	)
	tentativas := tentativasPorAcao(acao)
	for tentativa := 1; tentativa <= tentativas; tentativa++ {
		resposta = protocol.TransactionResponse{}
		err = ChamarJSON(
			http.MethodPost, urlBase, "/v1/transactions/"+acao,
			nil, req, &resposta, TimeoutPadrao,
		)
		if err == nil {
			break
		}
		// Resposta HTTP de erro não melhora ao repetir; só falhas de rede.
		if _, respondeu := err.(*ErroHTTP); respondeu {
			break
		}
		if tentativa < tentativas {
			time.Sleep(time.Duration(tentativa) * 300 * time.Millisecond)
		}
	}
	if err != nil {
		return resposta, fmt.Errorf("servidor %s: %w", servidor, err)
	}

	if resposta.TransactionID != req.TransactionID {
		return resposta, fmt.Errorf("resposta com ID de transação inesperado de %s", servidor)
	}
	return resposta, nil
}
