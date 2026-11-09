package consorcio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mclara9593/caronasDistribuidas/internal/protocol"
)

const (
	// TimeoutBusca é curto de propósito: se um peer estiver fora do ar, a
	// busca distribuída segue com os demais em vez de ficar travada.
	TimeoutBusca = 3 * time.Second
	// TimeoutPadrao vale para consultas e para as mensagens do 2PC.
	TimeoutPadrao = 5 * time.Second
)

// clienteHTTP é compartilhado por todas as chamadas entre servidores, para
// reaproveitar conexões (keep-alive) em vez de abrir uma nova a cada envio.
var clienteHTTP = func() *http.Client {
	transporte := http.DefaultTransport.(*http.Transport).Clone()
	transporte.MaxIdleConnsPerHost = 16
	transporte.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transporte}
}()

// PeersRegistry mantém o mapeamento entre o id de cada servidor do
// consórcio e a URL base onde a sua API REST pode ser alcançada.
type PeersRegistry struct {
	mu    sync.RWMutex
	peers map[string]string // idServidor -> URL base REST
}

// NovoPeersRegistry cria um registro a partir do mapa de servidores
// conhecidos. Retorna ponteiro: o registro contém um mutex e não pode ser
// copiado.
func NovoPeersRegistry(peersIniciais map[string]string) *PeersRegistry {
	copia := make(map[string]string, len(peersIniciais))
	for id, base := range peersIniciais {
		copia[id] = strings.TrimRight(base, "/")
	}
	return &PeersRegistry{peers: copia}
}

// URLDoServidor retorna a URL base REST de um servidor e se ele é conhecido.
func (p *PeersRegistry) URLDoServidor(idServidor string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	base, existe := p.peers[idServidor]
	return base, existe
}

// IDs devolve os ids dos peers em ordem alfabética (ordem determinística).
func (p *PeersRegistry) IDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.peers))
	for id := range p.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ErroHTTP indica que o peer respondeu, mas com status diferente de 2xx.
// Diferente de um erro de rede, repetir a mesma mensagem não ajuda.
type ErroHTTP struct {
	Status int
	Corpo  string
}

func (e *ErroHTTP) Error() string {
	if e.Corpo == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Corpo)
}

// ChamarJSON faz uma requisição a um servidor do consórcio. Se corpo != nil
// ele é enviado como JSON; se saida != nil a resposta é decodificada nela.
func ChamarJSON(
	metodo, urlBase, caminho string,
	query url.Values,
	corpo, saida interface{},
	timeout time.Duration,
) error {
	endpoint := strings.TrimRight(urlBase, "/") + caminho
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var leitor io.Reader
	if corpo != nil {
		bytesCorpo, err := json.Marshal(corpo)
		if err != nil {
			return fmt.Errorf("serializar requisição: %w", err)
		}
		leitor = bytes.NewReader(bytesCorpo)
	}

	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, metodo, endpoint, leitor)
	if err != nil {
		return err
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := clienteHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &ErroHTTP{Status: resp.StatusCode, Corpo: strings.TrimSpace(string(trecho))}
	}

	if saida == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(saida); err != nil {
		return fmt.Errorf("decodificar resposta: %w", err)
	}
	return nil
}

// ResultadoPeer é a resposta (ou o erro) de um peer num envio em broadcast.
type ResultadoPeer[T any] struct {
	Peer  string
	Valor T
	Err   error
}

// Broadcast executa fn para todos os peers ao mesmo tempo (A -> B e C em
// paralelo) e espera todos terminarem. Um peer lento ou fora do ar não impede
// o resultado dos outros: cada um tem seu próprio erro. O resultado vem na
// ordem alfabética dos ids.
func Broadcast[T any](
	peers *PeersRegistry,
	fn func(idPeer, urlBase string) (T, error),
) []ResultadoPeer[T] {
	ids := peers.IDs()
	resultados := make([]ResultadoPeer[T], len(ids))

	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			base, _ := peers.URLDoServidor(id)
			valor, err := fn(id, base)
			resultados[i] = ResultadoPeer[T]{Peer: id, Valor: valor, Err: err}
		}(i, id)
	}
	wg.Wait()

	return resultados
}

// ConsultarTrechosRemotos pergunta a um servidor quais trechos locais ele tem
// com assentos livres. origem e destino são filtros opcionais: vazios devolvem
// todos os trechos disponíveis (necessário para montar itinerários com
// várias paradas). Os IDs voltam no formato idTrecho@idServidor.
func ConsultarTrechosRemotos(urlBase, origem, destino string) ([]protocol.RemoteSection, error) {
	query := url.Values{}
	if origem != "" {
		query.Set("origem", origem)
	}
	if destino != "" {
		query.Set("destino", destino)
	}

	var trechos []protocol.RemoteSection
	if err := ChamarJSON(http.MethodGet, urlBase, "/consorcio/trechos", query, nil, &trechos, TimeoutBusca); err != nil {
		return nil, fmt.Errorf("servidor %s: %w", urlBase, err)
	}
	return trechos, nil
}
