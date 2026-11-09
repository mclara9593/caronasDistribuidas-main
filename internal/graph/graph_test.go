package graph

import (
	"testing"
)

func TestAddRideAssentosPorTrecho(t *testing.T) {
	g := NovoGrafo()
	rota := []string{"Feira", "Salvador", "Aracaju"}
	assentos := []int{3, 1}
	precos := []float64{25.0, 50.0}

	err := g.AddRide("ride-1", rota, assentos, precos, "motorista@teste.com")
	if err != nil {
		t.Fatalf("erro inesperado ao adicionar carona: %v", err)
	}

	ride, existe := g.Rides["ride-1"]
	if !existe {
		t.Fatalf("carona não encontrada no grafo")
	}

	if len(ride.Sections) != 2 {
		t.Fatalf("esperava 2 seções, obteve %d", len(ride.Sections))
	}

	if ride.Sections[0].Assentos != 3 {
		t.Errorf("esperava 3 assentos no primeiro trecho, obteve %d", ride.Sections[0].Assentos)
	}
	if ride.Sections[1].Assentos != 1 {
		t.Errorf("esperava 1 assento no segundo trecho, obteve %d", ride.Sections[1].Assentos)
	}

	// Testa validação com tamanho incompatível
	errInvalido := g.AddRide("ride-2", rota, []int{3}, precos, "motorista@teste.com")
	if errInvalido == nil {
		t.Errorf("esperava erro por tamanho incompatível de assentos")
	}

	// Testa assento <= 0
	errZero := g.AddRide("ride-3", rota, []int{3, 0}, precos, "motorista@teste.com")
	if errZero == nil {
		t.Errorf("esperava erro por assento menor ou igual a zero")
	}
}

func TestSearchRouteDFSSemRepeticao(t *testing.T) {
	g := NovoGrafo()
	// Rota com ciclo possível: A-B-C-A-D e caminhos alternativos até D.
	g.AddRide("r1", []string{"A", "B", "C", "D"}, []int{2, 2, 2}, []float64{10, 10, 10}, "m@t.com")
	g.AddRide("r2", []string{"A", "D"}, []int{2}, []float64{50}, "m@t.com")
	g.AddRide("r3", []string{"C", "A"}, []int{2}, []float64{5}, "m@t.com") // fecha ciclo A-B-C-A
	g.AddRide("r4", []string{"B", "D"}, []int{0 + 1}, []float64{15}, "m@t.com")

	res := g.SearchRoute("A", "D")

	// Esperado: A-D (1 trecho), A-B-D (2 trechos), A-B-C-D (3 trechos).
	if len(res) != 3 {
		t.Fatalf("esperava 3 itinerários, obteve %d", len(res))
	}
	for i, esperado := range []int{1, 2, 3} {
		if len(res[i].Sections) != esperado {
			t.Errorf("itinerário %d: esperava %d trechos, obteve %d", i, esperado, len(res[i].Sections))
		}
	}

	vistos := map[string]bool{}
	for _, ride := range res {
		chave := chaveDoCaminho(ride.Sections)
		if vistos[chave] {
			t.Errorf("itinerário repetido: %s", chave)
		}
		vistos[chave] = true

		cidades := map[string]bool{"A": true}
		for _, s := range ride.Sections {
			if cidades[s.Destino] {
				t.Errorf("cidade %s repetida em %s", s.Destino, chave)
			}
			cidades[s.Destino] = true
		}
	}
}
