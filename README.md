<h3 align="center">VAIJUNTO</h3>

<p align="center">
  Sistema de caronas
</p>

<div align="center">

![Status](https://img.shields.io/badge/Status-Etapa%203%20-green)
![Language](https://img.shields.io/badge/Linguagem-GO-blue)

</div>

<div align="center">

[Sobre o projeto](#sobre-o-projeto) 
• [Requisitos](#requisitos) 
• [Instalação](#instalação) 
• [Solução](#solução)
• [Arquitetura](#arquitetura) 
• [Estrutura e ferramentas](#estruturas-e-ferramentas-utilizadas) 
• [Fluxo de Dados](#fluxo-de-dados) 
• [Testes e Resultados](#testes-e-resultados) 

</div>

## 📄Sobre o Projeto
Uma startup de mobilidade deseja lançar um serviço de caronas compartilhadas para viagens de média e
longa distância. Atualmente, a empresa mantém um servidor central, onde os motoristas publicam as
caronas que pretendem realizar e os passageiros consultam e reservam assentos pela Internet, sem
qualquer intermediação humana.

### Funcionais
| Requisito | Descrição |
| --- | --- |
| Autenticação de usuário | Cadastro e login de passageiros via email e senha |
| Autenticação de motorista | Cadastro e login de motoristas via email, senha e CNH |
| Publicação de carona | Publicação de carona contendo rota, capacidade de assentos e preço por Section |
| Consulta de carona | Motorista pode consultar as caronas já publicadas por ele |
| Cancelar carona | O motorista pode cancelar uma carona, removendo os trechos correspondentes do grafo |
| Buscar itinerários | O usuário busca itinerários entre uma origem e um destino, podendo combinar trechos de caronas diferentes |
| Confirmação de reserva | O usuário pode confirmar a reserva de um itinerário composto por um ou mais Sections, usando um único ID de rota |
| Consulta de reserva | *(em desenvolvimento)* |
| Cancelar reserva | Usuário pode cancelar uma reserva, liberando as vagas dos trechos envolvidos |

### Não Funcionais
| Requisito | Descrição |
| --- | --- |
| Interoperabilidade | Comunicação via protocolo próprio em JSON sobre TCP, independente de linguagem/cliente |
| Concorrência | Acesso seguro ao grafo compartilhado entre múltiplas conexões simultâneas, via mutex de leitura/escrita |
| Portabilidade | Execução do servidor via Docker, sem depender de instalação manual do Go na máquina que o hospeda |

## Requisitos
* Go 1.21 ou superior instalado (para rodar servidor e clientes localmente)
* Docker (opcional, caso prefira rodar o servidor em container)
* Conectividade de rede entre as máquinas dos clientes e o servidor (mesma rede local ou IP acessível)

## 📦 Execução

### 1. Clonar o repositório
```bash
git clone https://github.com/mclara9593/caronasDistribuidas
cd caronasDistribuidas
```

### 2. Rodando via Docker

Construindo as iamgens
```bash
docker build -f dockerfile --target motorista -t caronas-motorista .
docker build -f dockerfile --target passageiro -t caronas-passageiro .
docker build -f dockerfile --target server -t caronas-server .
```

Servidor A
```bash
docker run -d --name servidor-a \
  -p 9593:9593 -p 8081:8081 \
  caronas-server \
  -id A -tcp :9593 -http :8081 \
 -peers "B=http://172.16.201.16:8081,C=http://172.16.201.3:8081"
```
Servidor B
```bash
docker run -d --name servidor-b \
  -p 9594:9594 -p 8081:8081 \
  caronas-server \
  -id B -tcp :9594 -http :8081 \
  -peers "A=http://172.16.201.2:8081,C=http://172.16.201.3:8081"

```

Servidor C
```bash
docker run -d --name servidor-c \
  -p 9595:9595 -p 8081:8081 \
  caronas-server \
  -id C -tcp :9595 -http :8081 \
  -peers "A=http://172.16.201.2:8081,B=http://172.16.201.16:8081"

```

Para acompanhar cada servidor:
```bash
docker logs -f servidor-a
```

Para rodar os clientes:

```bash
docker run --rm -it caronas-motorista
```


```bash
docker run --rm -it caronas-passageiro
```



## 💻 Solução

### Protocolo de comunicação cliente-servidor
Comunicação em JSON sobre TCP, com cada requisição seguindo um envelope padrão com ação, ID único da requisição e payload específico. Cada resposta do servidor traz o status (`SUCCESS`/`ERROR`), uma mensagem e um payload opcional. As mensagens são delimitadas por quebra de linha (`\n`), permitindo múltiplas requisições na mesma conexão TCP persistente.

### Roteamento por grafos
Cada cidade é um nó do grafo, e cada trecho de carona publicado (Section) é uma aresta direcionada entre duas cidades, com capacidade de assentos e preço próprios. Ao publicar uma carona com uma rota de múltiplas cidades, ela é quebrada automaticamente em trechos (Sections) individuais.

### Algoritmos de busca


## Domínio da solução
* Disponibilidade de assentos deve ser cobrada por Section
* Passageiro que confirma primeiro recebe preferência
* Ao conectar o Section A→B (Carona X) ao Section B→C (Carona Y), a partida da segunda carona ocorre após a chegada da primeira: Horário de partida B→C > Horário de partida A→B

## 👩🏻‍💻 Arquitetura 

### 🖧 Conexão
**Goroutine**: Interface de software pela qual um processo envia e recebe mensagens de e para a rede (socket TCP).
* É orientado a conexões ponto a ponto e full-duplex, onde cada socket de conexão ativa é identificado por uma tupla de **quatro elementos**: (*endereço IP de origem, porta de origem, endereço IP de destino, porta de destino*)
* É criado um socket para cada cliente, tratado em uma goroutine isolada, permitindo múltiplos motoristas e passageiros conectados simultaneamente

### Concorrência
Lock / Unlock → trava exclusiva, para escrita. Só uma goroutine por vez pode ter essa trava, e enquanto ela está travada, ninguém mais (nem leitor, nem escritor) consegue acessar.
RLock / RUnlock → trava de leitura. Várias goroutines podem ter essa trava ao mesmo tempo, desde que ninguém esteja com a trava de escrita.

Essas travas protegem o grafo compartilhado (rotas, trechos e assentos) contra condições de corrida quando múltiplos passageiros tentam reservar ao mesmo tempo.

Esqueleto da requisição:
```json
{
  "action": "NOME_DA_OPERACAO",
  "request_id": "ID_UNICO_DA_REQUISICAO",
  "payload": {
    "campo_especifico_1": "valor",
    "campo_especifico_2": "valor"
  }
}
```

Esqueleto da resposta:
```json
{
  "request_id": "ID_UNICO_DA_REQUISICAO",
  "status": "SUCCESS",
  "message": "Descrição do resultado",
  "payload": {}
}
```

## 🛠 Estruturas e ferramentas utilizadas 
* Docker
* Go
* TCP/IP Protocol

## Fluxo de Dados
1. O cliente (motorista ou passageiro) estabelece uma conexão TCP persistente com o servidor central.
2. A cada ação do usuário (cadastro, login, publicar carona, buscar itinerário, reservar, etc.), o cliente monta um envelope JSON com `action`, `request_id` e `payload`, e envia pela conexão.
3. O servidor lê a requisição, identifica a ação e a encaminha para o handler correspondente.
4. O handler valida os dados, aciona o grafo de rotas (quando aplicável) sob proteção de mutex, e monta uma resposta.
5. A resposta é serializada em JSON e enviada de volta ao cliente pela mesma conexão.
6. A conexão permanece aberta, permitindo múltiplas requisições sequenciais até o cliente encerrar a sessão.


## ✍️ Colaboradores

Este projeto foi desenvolvido por:

- [**Maria Clara**](https://github.com/mclara9593)