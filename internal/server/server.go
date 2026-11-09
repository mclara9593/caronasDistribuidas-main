package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mclara9593/caronasDistribuidas/internal/consorcio"
	"github.com/mclara9593/caronasDistribuidas/internal/domain"
	"github.com/mclara9593/caronasDistribuidas/internal/graph"
	"github.com/mclara9593/caronasDistribuidas/internal/persistence"
	"github.com/mclara9593/caronasDistribuidas/internal/protocol"
)

// SectionResposta é o trecho anunciado a outros servidores em /consorcio/trechos.
type SectionResposta = protocol.RemoteSection

type Config struct {
	IDServidor string
	TCPAddr    string
	HTTPAddr   string
	Peers      map[string]string
}

type Server struct {
	idServidor string

	grafo           *graph.Grafo
	repoPassageiros *domain.Repositorio
	repoMotoristas  *domain.Repositorio

	tcpAddr       string
	httpAddr      string
	relogio       *consorcio.RelogioLogico
	peersRegistry *consorcio.PeersRegistry
}

func NovoServidor(cfg Config) *Server {
	return &Server{
		idServidor:      cfg.IDServidor,
		tcpAddr:         cfg.TCPAddr,
		httpAddr:        cfg.HTTPAddr,
		grafo:           graph.NovoGrafo(),
		repoPassageiros: domain.NovoRepositorio(),
		repoMotoristas:  domain.NovoRepositorio(),
		relogio:         consorcio.NovoRelogio(cfg.IDServidor),
		peersRegistry:   consorcio.NovoPeersRegistry(cfg.Peers),
	}
}

func (s *Server) Iniciar() error {
	if err := persistence.CarregarEstado(s.idServidor, s.grafo); err != nil {
		return fmt.Errorf("carregar backup local: %w", err)
	}

	tcpListener, err := net.Listen("tcp", s.tcpAddr)
	if err != nil {
		return fmt.Errorf("erro ao iniciar TCP em %s: %w", s.tcpAddr, err)
	}
	defer tcpListener.Close()

	mux := http.NewServeMux()
	s.registrarRotasHTTP(mux)

	httpServer := &http.Server{
		Addr:              s.httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Erro no listener REST: %v\n", err)
		}
	}()

	fmt.Printf("Empresa %s: TCP %s, REST %s\n", s.idServidor, s.tcpAddr, s.httpAddr)

	for {
		conn, err := tcpListener.Accept()
		if err != nil {
			fmt.Printf("Erro ao aceitar conexão TCP: %v\n", err)
			continue
		}
		go s.tratarCliente(conn)
	}
}

func (s *Server) salvarEstado() error {
	return persistence.SalvarEstado(s.idServidor, s.grafo)
}

func (s *Server) tratarCliente(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		linha, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Erro de leitura do cliente %s: %v\n", conn.RemoteAddr(), err)
			}
			return
		}

		var req protocol.Request
		if err := json.Unmarshal(linha, &req); err != nil {
			s.enviarErro(conn, "", "Formato JSON inválido")
			continue
		}
		s.rotearRequisicao(conn, &req)
	}
}

func (s *Server) rotearRequisicao(conn net.Conn, req *protocol.Request) {
	switch req.Action {
	case "cadastrar_user":
		s.handleCadastrarUser(conn, req)
	case "cadastrar_motorista":
		s.handleCadastrarMotorista(conn, req)
	case "auth_user":
		s.handleAuthUser(conn, req)
	case "auth_conductor":
		s.handleAuthConductor(conn, req)
	case "publish_ride":
		s.handlePublishRide(conn, req)
	case "cancel_ride":
		s.handleCancelRide(conn, req)
	case "get_my_rides":
		s.handleGetMyRides(conn, req)
	case "search_route":
		s.handleSearchRoute(conn, req)
	case "book_ride":
		s.handleBookRide(conn, req)
	case "get_bookings":
		s.handleGetBookings(conn, req)
	case "cancel_booking":
		s.handleCancelBooking(conn, req)
	default:
		s.enviarErro(conn, req.RequestID, "Ação não reconhecida pelo servidor")
	}
}

func (s *Server) registrarRotasHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/consorcio/trechos", s.handleConsultarTrechos)
	mux.HandleFunc("/v1/search", s.handlePeerSearch)
	mux.HandleFunc("/v1/transactions/prepare", s.handlePeerPrepare)
	mux.HandleFunc("/v1/transactions/commit", s.handlePeerCommit)
	mux.HandleFunc("/v1/transactions/abort", s.handlePeerAbort)
	mux.HandleFunc("/v1/transactions/prepare-cancel", s.handlePeerPrepareCancel)
	mux.HandleFunc("/v1/transactions/commit-cancel", s.handlePeerCommitCancel)
	mux.HandleFunc("/v1/transactions/abort-cancel", s.handlePeerAbortCancel)
	mux.HandleFunc("/v1/bookings", s.handlePeerBookings)
	mux.HandleFunc("/v1/ride-bookings", s.handlePeerRideBookings)
}

// handleConsultarTrechos devolve os trechos locais com assentos livres.
// origem e destino são filtros opcionais; sem eles, devolve todos os trechos
// (é o que permite a outro servidor montar itinerários com várias paradas).
func (s *Server) handleConsultarTrechos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	origem := r.URL.Query().Get("origem")
	destino := r.URL.Query().Get("destino")

	estado := s.grafo.Exportar()
	resposta := make([]SectionResposta, 0)
	for _, trechos := range estado.Nodes {
		for _, trecho := range trechos {
			if trecho.Assentos <= 0 ||
				(origem != "" && trecho.Origem != origem) ||
				(destino != "" && trecho.Destino != destino) {
				continue
			}
			resposta = append(resposta, SectionResposta{
				SectionID: protocol.ComporSectionID(trecho.IdSection, s.idServidor),
				Origem:    trecho.Origem,
				Destino:   trecho.Destino,
				Assentos:  trecho.Assentos,
				Preco:     trecho.Preco,
			})
		}
	}
	sort.Slice(resposta, func(i, j int) bool { return resposta[i].SectionID < resposta[j].SectionID })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resposta)
}

func (s *Server) handleCadastrarUser(conn net.Conn, req *protocol.Request) {
	var payload protocol.CadastroPassageiro
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de cadastro inválido")
		return
	}
	if payload.Nome == "" || payload.Email == "" || payload.Senha == "" {
		s.enviarErro(conn, req.RequestID, "Nome, email e senha são obrigatórios")
		return
	}

	usuario := domain.Usuario{Nome: payload.Nome, Email: payload.Email, Senha: payload.Senha}
	if !s.repoPassageiros.Cadastrar(usuario) {
		s.enviarErro(conn, req.RequestID, "Já existe um passageiro cadastrado com este email")
		return
	}
	s.enviarSucesso(conn, req.RequestID, "Passageiro cadastrado com sucesso! Faça login para continuar.", nil)
}

func (s *Server) handleCadastrarMotorista(conn net.Conn, req *protocol.Request) {
	var payload protocol.CadastroMotorista
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de cadastro inválido")
		return
	}
	if payload.Nome == "" || payload.Email == "" || payload.Senha == "" || payload.CNH == "" {
		s.enviarErro(conn, req.RequestID, "Nome, email, senha e CNH são obrigatórios")
		return
	}

	usuario := domain.Usuario{
		Nome: payload.Nome, Email: payload.Email, Senha: payload.Senha, CNH: payload.CNH,
	}
	if !s.repoMotoristas.Cadastrar(usuario) {
		s.enviarErro(conn, req.RequestID, "Já existe um motorista cadastrado com este email")
		return
	}
	s.enviarSucesso(conn, req.RequestID, "Motorista cadastrado com sucesso! Faça login para continuar.", nil)
}

func (s *Server) handleAuthUser(conn net.Conn, req *protocol.Request) {
	var payload protocol.LoginRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de login inválido")
		return
	}
	usuario, ok := s.repoPassageiros.ValidarLogin(payload.Email, payload.Senha)
	if !ok {
		s.enviarErro(conn, req.RequestID, "Email ou senha inválidos")
		return
	}
	s.enviarSucesso(conn, req.RequestID, "Passageiro autenticado com sucesso!", protocol.LoginResult{Nome: usuario.Nome})
}

func (s *Server) handleAuthConductor(conn net.Conn, req *protocol.Request) {
	var payload protocol.LoginRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de login inválido")
		return
	}
	usuario, ok := s.repoMotoristas.ValidarLogin(payload.Email, payload.Senha)
	if !ok {
		s.enviarErro(conn, req.RequestID, "Email ou senha inválidos")
		return
	}
	s.enviarSucesso(conn, req.RequestID, "Motorista autenticado com sucesso!", protocol.LoginResult{Nome: usuario.Nome})
}

func (s *Server) handlePublishRide(conn net.Conn, req *protocol.Request) {
	var payload protocol.PushRide
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de publicação inválido")
		return
	}

	rideID := "ride-" + req.RequestID
	if err := s.grafo.AddRide(rideID, payload.Route, payload.Assentos, payload.Precos, payload.DriverEmail); err != nil {
		s.enviarErro(conn, req.RequestID, err.Error())
		return
	}
	if err := s.salvarEstado(); err != nil {
		s.enviarErro(conn, req.RequestID, "Carona criada, mas não foi possível salvar o backup: "+err.Error())
		return
	}
	s.enviarSucesso(conn, req.RequestID, "Carona publicada com sucesso!", protocol.PublishRideResult{RideID: rideID})
}

func (s *Server) handleCancelRide(conn net.Conn, req *protocol.Request) {
	var payload protocol.GetID
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.RideID == "" {
		s.enviarErro(conn, req.RequestID, "Payload inválido ou ID da carona ausente")
		return
	}

	ride, existe := s.grafo.RideByID(payload.RideID)
	if !existe {
		s.enviarErro(conn, req.RequestID, fmt.Sprintf("carona %q não encontrada", payload.RideID))
		return
	}

	porReserva := make(map[string]*protocol.BookingInfo)

	agregar := func(reservas []protocol.BookingInfo) {
		for _, reserva := range reservas {
			if reserva.ReservationID == "" {
				continue
			}

			atual, existe := porReserva[reserva.ReservationID]
			if !existe {
				copia := reserva
				copia.SectionIDs = append([]string(nil), reserva.SectionIDs...)
				porReserva[reserva.ReservationID] = &copia
				continue
			}

			// Evita repetir trechos quando uma reserva aparece em mais
			// de um servidor ou em mais de uma resposta.
			seen := make(map[string]bool, len(atual.SectionIDs))
			for _, id := range atual.SectionIDs {
				seen[id] = true
			}
			for _, id := range reserva.SectionIDs {
				if !seen[id] {
					atual.SectionIDs = append(atual.SectionIDs, id)
					seen[id] = true
				}
			}
		}
	}

	sectionIDsCarona := make([]string, 0, len(ride.Sections))
	for _, section := range ride.Sections {
		sectionIDsCarona = append(sectionIDsCarona, section.IdSection)
	}

	// Inclui as reservas locais que usam trechos desta carona.
	locais := s.grafo.ReservationsUsingSections(sectionIDsCarona)
	for _, reserva := range locais {
		idsComServidor := make([]string, 0, len(reserva.SectionIDs))
		for _, id := range reserva.SectionIDs {
			idsComServidor = append(idsComServidor, protocol.ComporSectionID(id, s.idServidor))
		}
		agregar([]protocol.BookingInfo{{
			ReservationID:  reserva.ReservationID,
			PassengerEmail: reserva.PassengerEmail,
			SectionIDs:     idsComServidor,
		}})
	}

	// Os trechos desta carona só existem aqui, então todas as reservas que os
	// usam já estão em porReserva. Os outros trechos dessas reservas ficam nos
	// peers: pedimos a todos, ao mesmo tempo e pelo ID da reserva, a parte
	// que cada um guarda.
	if len(porReserva) > 0 {
		ids := make([]string, 0, len(porReserva))
		for id := range porReserva {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		resultados := consorcio.Broadcast(s.peersRegistry,
			func(_, urlBase string) ([]protocol.BookingInfo, error) {
				var lista []protocol.BookingInfo
				err := consorcio.ChamarJSON(
					http.MethodPost, urlBase, "/v1/ride-bookings", nil,
					protocol.RideBookingsRequest{ReservationIDs: ids}, &lista, consorcio.TimeoutPadrao,
				)
				return lista, err
			})

		for _, r := range resultados {
			if r.Err != nil {
				s.enviarErro(conn, req.RequestID,
					fmt.Sprintf("não foi possível consultar reservas no servidor %s: %v", r.Peer, r.Err))
				return
			}
			agregar(qualificarReservas(r.Valor, r.Peer))
		}
	}

	// Cada reserva é cancelada por inteiro, inclusive os trechos que não
	// pertencem à carona que o motorista está removendo.
	for _, reserva := range porReserva {
		itinerario := make([]consorcio.ItemItinerario, 0, len(reserva.SectionIDs))
		for _, sectionID := range reserva.SectionIDs {
			itinerario = append(itinerario, consorcio.ItemItinerario{
				SectionID: sectionID,
				Assentos:  1,
			})
		}

		if err := consorcio.CoordenarCancelamento(
			reserva.ReservationID,
			reserva.PassengerEmail,
			itinerario,
			s.idServidor,
			s.relogio,
			s.grafo,
			s.peersRegistry,
		); err != nil {
			s.enviarErro(conn, req.RequestID,
				fmt.Sprintf("não foi possível cancelar a reserva %s: %v", reserva.ReservationID, err))
			return
		}
	}

	// RemoveRide ainda valida que não restaram reservas ou transações pendentes.
	if err := s.grafo.RemoveRide(payload.RideID); err != nil {
		s.enviarErro(conn, req.RequestID, err.Error())
		return
	}
	if err := s.salvarEstado(); err != nil {
		s.enviarErro(conn, req.RequestID,
			"carona removida, mas não foi possível salvar o backup: "+err.Error())
		return
	}

	s.enviarSucesso(conn, req.RequestID,
		"Carona removida e reservas associadas canceladas.", nil)
}

func (s *Server) handleGetMyRides(conn net.Conn, req *protocol.Request) {
	var payload protocol.GetMyRidesRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.Email == "" {
		s.enviarErro(conn, req.RequestID, "Payload inválido ou email do motorista ausente")
		return
	}
	rides := s.grafo.RidesByDriver(payload.Email)
	s.enviarSucesso(conn, req.RequestID, fmt.Sprintf("%d carona(s) encontrada(s)", len(rides)), rides)
}

func (s *Server) handleSearchRoute(conn net.Conn, req *protocol.Request) {
	var payload protocol.SearchRoute
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de busca inválido")
		return
	}

	// Este servidor pergunta a todos os peers ao mesmo tempo e busca em
	// profundidade sobre o grafo local somado aos trechos deles.
	remotos, indisponiveis := s.coletarTrechosRemotos()
	resultados := s.grafo.SearchRouteComRemotos(payload.Source, payload.Destination, remotos)

	resposta := make([]protocol.RideInfo, 0, len(resultados))
	for _, ride := range resultados {
		trechos := make([]protocol.SectionInfo, 0, len(ride.Sections))

		for _, trecho := range ride.Sections {
			empresa := s.idServidor
			if _, dono, err := protocol.DecomporSectionID(trecho.IdSection); err == nil {
				empresa = dono
			}
			trechos = append(trechos, protocol.SectionInfo{
				IdSection: trecho.IdSection,
				Origem:    trecho.Origem,
				Destino:   trecho.Destino,
				Assentos:  trecho.Assentos,
				Preco:     trecho.Preco,
				Empresa:   empresa,
			})
		}

		resposta = append(resposta, protocol.RideInfo{
			IdRide:     ride.IdRide,
			Sections:   trechos,
			IdDriver:   ride.IdDriver,
			TotalPrice: ride.TotalPrice,
		})
	}

	msg := "Itinerários encontrados"
	if len(indisponiveis) > 0 {
		msg += fmt.Sprintf(" (resultado parcial: sem resposta de %s)", strings.Join(indisponiveis, ", "))
	}
	s.enviarSucesso(conn, req.RequestID, msg, resposta)
}

// coletarTrechosRemotos pede os trechos disponíveis a todos os peers em
// paralelo. Um peer fora do ar não impede a busca: ele só é listado em
// `indisponiveis` e a busca segue com os demais.
func (s *Server) coletarTrechosRemotos() (trechos []graph.Section, indisponiveis []string) {
	resultados := consorcio.Broadcast(s.peersRegistry,
		func(_, urlBase string) ([]protocol.RemoteSection, error) {
			return consorcio.ConsultarTrechosRemotos(urlBase, "", "")
		})

	for _, r := range resultados {
		if r.Err != nil {
			fmt.Printf("Busca: servidor %s indisponível: %v\n", r.Peer, r.Err)
			indisponiveis = append(indisponiveis, r.Peer)
			continue
		}
		for _, t := range r.Valor {
			if t.SectionID == "" || t.Assentos <= 0 {
				continue
			}
			trechos = append(trechos, graph.Section{
				IdSection: t.SectionID,
				Origem:    t.Origem,
				Destino:   t.Destino,
				Assentos:  t.Assentos,
				Preco:     t.Preco,
			})
		}
	}
	return trechos, indisponiveis
}

// reservasDosPeers pergunta a todos os peers, em paralelo, as reservas de um
// passageiro. Os IDs de trecho voltam qualificados (idTrecho@idServidor).
func (s *Server) reservasDosPeers(email string) (reservas []protocol.BookingInfo, falhas []string) {
	resultados := consorcio.Broadcast(s.peersRegistry,
		func(_, urlBase string) ([]protocol.BookingInfo, error) {
			var lista []protocol.BookingInfo
			err := consorcio.ChamarJSON(
				http.MethodGet, urlBase, "/v1/bookings",
				url.Values{"passenger_email": {email}}, nil, &lista, consorcio.TimeoutPadrao,
			)
			return lista, err
		})

	for _, r := range resultados {
		if r.Err != nil {
			falhas = append(falhas, fmt.Sprintf("%s (%v)", r.Peer, r.Err))
			continue
		}
		reservas = append(reservas, qualificarReservas(r.Valor, r.Peer)...)
	}
	return reservas, falhas
}

// qualificarReservas garante que todos os IDs de trecho tenham o formato idTrecho@idServidor.
func qualificarReservas(reservas []protocol.BookingInfo, servidor string) []protocol.BookingInfo {
	for i := range reservas {
		reservas[i].SectionIDs = qualificarIDsTrechos(reservas[i].SectionIDs, servidor)
	}
	return reservas
}

func (s *Server) handleBookRide(conn net.Conn, req *protocol.Request) {
	var payload protocol.BookRideRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de reserva inválido")
		return
	}
	if payload.RideID == "" || payload.PassengerEmail == "" {
		s.enviarErro(conn, req.RequestID, "ID do itinerário e email do passageiro são obrigatórios")
		return
	}

	sectionIDs := s.grafo.ResolveItinerary(payload.RideID)
	if len(sectionIDs) == 0 {
		s.enviarErro(conn, req.RequestID, "Itinerário sem trechos")
		return
	}

	itinerario := make([]consorcio.ItemItinerario, 0, len(sectionIDs))
	for _, id := range sectionIDs {
		// Esta busca é local. Itinerários distribuídos precisam preservar
		// o dono de cada trecho no resultado da busca (idTrecho@idServidor).
		if _, _, err := protocol.DecomporSectionID(id); err != nil {
			id = protocol.ComporSectionID(id, s.idServidor)
		}
		itinerario = append(itinerario, consorcio.ItemItinerario{SectionID: id, Assentos: 1})
	}

	reservationID, err := consorcio.CoordenarReserva(
		itinerario,
		payload.PassengerEmail,
		s.idServidor,
		s.relogio,
		s.grafo,
		s.peersRegistry,
	)
	if err != nil {
		s.enviarErro(conn, req.RequestID, err.Error())
		return
	}
	if err := s.salvarEstado(); err != nil {
		s.enviarErro(conn, req.RequestID, "Reserva processada, mas não foi possível salvar o backup: "+err.Error())
		return
	}

	s.enviarSucesso(conn, req.RequestID, "Reserva confirmada!", protocol.BookRideResult{ReservationID: reservationID})
}

func (s *Server) handleGetBookings(conn net.Conn, req *protocol.Request) {
	var payload protocol.GetBookingsRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.PassengerEmail == "" {
		s.enviarErro(conn, req.RequestID, "Email do passageiro ausente ou payload inválido")
		return
	}

	// O mesmo ID de reserva é usado nos servidores que guardam os trechos
	// da mesma reserva. Por isso, juntamos os trechos por ReservationID.
	porReserva := make(map[string]*protocol.BookingInfo)

	agregar := func(reservas []protocol.BookingInfo) {
		for _, reserva := range reservas {
			existente, ok := porReserva[reserva.ReservationID]
			if !ok {
				copia := reserva
				copia.SectionIDs = append([]string(nil), reserva.SectionIDs...)
				porReserva[reserva.ReservationID] = &copia
				continue
			}
			existente.SectionIDs = append(existente.SectionIDs, reserva.SectionIDs...)
		}
	}

	locais := s.grafo.ReservationsByPassenger(payload.PassengerEmail)
	reservasLocais := make([]protocol.BookingInfo, 0, len(locais))

	for _, reserva := range locais {
		reservasLocais = append(reservasLocais, protocol.BookingInfo{
			ReservationID:  reserva.ReservationID,
			PassengerEmail: reserva.PassengerEmail,
			SectionIDs:     qualificarIDsTrechos(reserva.SectionIDs, s.idServidor),
		})
	}

	agregar(reservasLocais)

	reservasPeers, falhas := s.reservasDosPeers(payload.PassengerEmail)
	agregar(reservasPeers)

	resultado := make([]protocol.BookingInfo, 0, len(porReserva))
	for _, reserva := range porReserva {
		resultado = append(resultado, *reserva)
	}

	msg := fmt.Sprintf("%d reserva(s) encontrada(s)", len(resultado))
	if len(falhas) > 0 {
		msg += fmt.Sprintf(" (resultado parcial: sem resposta de %s)", strings.Join(falhas, ", "))
	}
	s.enviarSucesso(conn, req.RequestID, msg, resultado)
}

func (s *Server) handleCancelBooking(conn net.Conn, req *protocol.Request) {
	var payload protocol.CancelBookingRequest
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		s.enviarErro(conn, req.RequestID, "Payload de cancelamento inválido")
		return
	}
	if payload.ReservationID == "" || payload.PassengerEmail == "" {
		s.enviarErro(conn, req.RequestID, "ID da reserva e email do passageiro são obrigatórios")
		return
	}

	porReserva := make(map[string]*protocol.BookingInfo)

	agregar := func(reservas []protocol.BookingInfo) {
		for _, reserva := range reservas {
			if reserva.ReservationID != payload.ReservationID ||
				reserva.PassengerEmail != payload.PassengerEmail {
				continue
			}

			atual, existe := porReserva[reserva.ReservationID]
			if !existe {
				copia := reserva
				copia.SectionIDs = append([]string(nil), reserva.SectionIDs...)
				porReserva[reserva.ReservationID] = &copia
				continue
			}

			seen := make(map[string]bool, len(atual.SectionIDs))
			for _, id := range atual.SectionIDs {
				seen[id] = true
			}
			for _, id := range reserva.SectionIDs {
				if !seen[id] {
					atual.SectionIDs = append(atual.SectionIDs, id)
					seen[id] = true
				}
			}
		}
	}

	locais := s.grafo.ReservationsByPassenger(payload.PassengerEmail)
	reservasLocais := make([]protocol.BookingInfo, 0, len(locais))
	for _, reserva := range locais {
		idsComServidor := make([]string, 0, len(reserva.SectionIDs))
		for _, id := range reserva.SectionIDs {
			idsComServidor = append(idsComServidor, protocol.ComporSectionID(id, s.idServidor))
		}
		reservasLocais = append(reservasLocais, protocol.BookingInfo{
			ReservationID:  reserva.ReservationID,
			PassengerEmail: reserva.PassengerEmail,
			SectionIDs:     idsComServidor,
		})
	}
	agregar(reservasLocais)

	// Cancelar só com parte dos trechos deixaria assentos presos em algum
	// peer; por isso, se algum servidor não responder, o cancelamento falha.
	reservasPeers, falhas := s.reservasDosPeers(payload.PassengerEmail)
	if len(falhas) > 0 {
		s.enviarErro(conn, req.RequestID,
			"não foi possível consultar o(s) servidor(es): "+strings.Join(falhas, ", "))
		return
	}
	agregar(reservasPeers)

	reserva, existe := porReserva[payload.ReservationID]
	if !existe || len(reserva.SectionIDs) == 0 {
		s.enviarErro(conn, req.RequestID, "Reserva não encontrada")
		return
	}

	itinerario := make([]consorcio.ItemItinerario, 0, len(reserva.SectionIDs))
	for _, sectionID := range reserva.SectionIDs {
		itinerario = append(itinerario, consorcio.ItemItinerario{
			SectionID: sectionID,
			Assentos:  1,
		})
	}

	if err := consorcio.CoordenarCancelamento(
		reserva.ReservationID,
		reserva.PassengerEmail,
		itinerario,
		s.idServidor,
		s.relogio,
		s.grafo,
		s.peersRegistry,
	); err != nil {
		s.enviarErro(conn, req.RequestID, err.Error())
		return
	}

	if err := s.salvarEstado(); err != nil {
		s.enviarErro(conn, req.RequestID,
			"reserva cancelada, mas não foi possível salvar o backup: "+err.Error())
		return
	}

	s.enviarSucesso(conn, req.RequestID, "Reserva cancelada com sucesso.", nil)
}

func (s *Server) handlePeerSearch(w http.ResponseWriter, r *http.Request) {
	origem := r.URL.Query().Get("origem")
	destino := r.URL.Query().Get("destino")
	if origem == "" || destino == "" {
		http.Error(w, "parâmetros 'origem' e 'destino' são obrigatórios", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.grafo.SearchRoute(origem, destino))
}

func (s *Server) handlePeerPrepare(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "prepare")
}
func (s *Server) handlePeerCommit(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "commit")
}
func (s *Server) handlePeerAbort(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "abort")
}
func (s *Server) handlePeerPrepareCancel(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "prepare-cancel")
}
func (s *Server) handlePeerCommitCancel(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "commit-cancel")
}
func (s *Server) handlePeerAbortCancel(w http.ResponseWriter, r *http.Request) {
	s.handlePeerTransaction(w, r, "abort-cancel")
}

func (s *Server) handlePeerTransaction(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req protocol.TransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}

	var resp protocol.TransactionResponse
	switch action {
	case "prepare":
		resp = consorcio.TratarPrepare(s.grafo, s.relogio, s.idServidor, req)
	case "commit":
		resp = consorcio.TratarCommit(s.grafo, s.relogio, s.idServidor, req)
	case "abort":
		resp = consorcio.TratarAbort(s.grafo, s.relogio, s.idServidor, req)
	case "prepare-cancel":
		resp = consorcio.TratarPrepareCancelamento(s.grafo, s.relogio, s.idServidor, req)
	case "commit-cancel":
		resp = consorcio.TratarCommitCancelamento(s.grafo, s.relogio, s.idServidor, req)
	case "abort-cancel":
		resp = consorcio.TratarAbortCancelamento(s.grafo, s.relogio, s.idServidor, req)
	default:
		http.Error(w, "ação transacional desconhecida", http.StatusBadRequest)
		return
	}

	// Persiste inclusive prepares e aborts: a pendência precisa sobreviver
	// a reinicializações. Se o backup falhar, não informa sucesso ao peer.
	if resp.Accepted {
		if err := s.salvarEstado(); err != nil {
			resp.Accepted = false
			resp.Reason = "não foi possível persistir o estado: " + err.Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) enviarSucesso(conn net.Conn, reqID, msg string, payload interface{}) {
	var rawPayload json.RawMessage
	if payload != nil {
		rawPayload, _ = json.Marshal(payload)
	}
	s.escreverResponse(conn, protocol.Response{
		RequestID: reqID,
		Status:    "SUCCESS",
		Message:   msg,
		Payload:   rawPayload,
	})
}

func (s *Server) enviarErro(conn net.Conn, reqID, msg string) {
	s.escreverResponse(conn, protocol.Response{
		RequestID: reqID,
		Status:    "ERROR",
		Message:   msg,
	})
}

func (s *Server) escreverResponse(conn net.Conn, resp protocol.Response) {
	bytes, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(bytes, '\n'))
}

func (s *Server) handlePeerBookings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	email := r.URL.Query().Get("passenger_email")
	if email == "" {
		http.Error(w, "parâmetro 'passenger_email' obrigatório", http.StatusBadRequest)
		return
	}

	reservas := s.grafo.ReservationsByPassenger(email)
	resposta := make([]protocol.BookingInfo, 0, len(reservas))

	for _, reserva := range reservas {
		resposta = append(resposta, protocol.BookingInfo{
			ReservationID:  reserva.ReservationID,
			PassengerEmail: reserva.PassengerEmail,

			SectionIDs: qualificarIDsTrechos(reserva.SectionIDs, s.idServidor),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resposta)
}

func (s *Server) handlePeerRideBookings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var payload protocol.RideBookingsRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
		(len(payload.SectionIDs) == 0 && len(payload.ReservationIDs) == 0) {
		http.Error(w, "payload inválido: informe section_ids ou reservation_ids", http.StatusBadRequest)
		return
	}

	var reservas []graph.Reservation
	if len(payload.SectionIDs) > 0 {
		reservas = append(reservas, s.grafo.ReservationsUsingSections(payload.SectionIDs)...)
	}
	if len(payload.ReservationIDs) > 0 {
		reservas = append(reservas, s.grafo.ReservationsByIDs(payload.ReservationIDs)...)
	}

	resultado := make([]protocol.BookingInfo, 0, len(reservas))
	incluidas := make(map[string]bool, len(reservas))
	for _, reserva := range reservas {
		if incluidas[reserva.ReservationID] {
			continue
		}
		incluidas[reserva.ReservationID] = true

		resultado = append(resultado, protocol.BookingInfo{
			ReservationID:  reserva.ReservationID,
			PassengerEmail: reserva.PassengerEmail,
			SectionIDs:     qualificarIDsTrechos(reserva.SectionIDs, s.idServidor),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resultado)
}

func qualificarIDsTrechos(sectionIDs []string, servidor string) []string {
	resultado := make([]string, 0, len(sectionIDs))

	for _, id := range sectionIDs {
		// Se já estiver no formato trecho@servidor, preserva o ID.
		if _, _, err := protocol.DecomporSectionID(id); err == nil {
			resultado = append(resultado, id)
			continue
		}

		resultado = append(resultado, protocol.ComporSectionID(id, servidor))
	}

	return resultado
}
