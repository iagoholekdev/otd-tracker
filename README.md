# OTD Tracker — WinnerBox

App local (Go + HTML/JS) que consulta o Azure DevOps via WIQL, puxa os PBIs
fechados e compara `Target Date` vs `Closed Date` pra saber se cada entrega
saiu no prazo, atrasada ou sem target definido. Também mostra o ciclo
(criação → fechamento).

Roda 100% na sua máquina: o Go faz as chamadas pro `dev.azure.com` direto
do servidor (sem CORS), e o front só fala com `localhost`. O PAT nunca é
salvo em disco, só usado na hora da consulta.

## Como rodar

Precisa do Go instalado (1.21+).

```bash
go run .
```

Abre em: http://localhost:8080

## Gerar o PAT (Personal Access Token)

1. Azure DevOps → ícone do usuário → **Personal Access Tokens**
2. **New Token**
3. Escopo mínimo: **Work Items (Read)**
4. Copia o token — ele só aparece uma vez

## Campos do formulário

- **Organização**: a parte da URL depois de `dev.azure.com/` (ex: `db1group`)
- **Team Project**: `WinnerBox`
- **PAT**: o token gerado acima
- **Assigned To (Target)**: quem tem que estar assinado nos work items
  filhos (Development/Code Review/Test Execution) — default já vem com
  `Iago Holek <iago.holek@db1.com.br>`
- **Fechado de / até**: range de `ClosedDate` do PBI
- **Entrega de Valor = SIM**: filtro extra opcional (`Custom.ENTREGA_DE_VALOR`)

## O que a query faz

Mesma WIQL que você passou, com os parâmetros acima interpolados:
busca PBIs fechados no Team Project que têm work items filhos do tipo
Development/Code Review/Test Execution, fechados, atribuídos à pessoa
informada. Depois busca os campos completos de cada PBI encontrado
(`workitemsbatch`) e calcula:

- **Drift**: `ClosedDate - TargetDate` em dias (negativo = adiantado,
  positivo = atrasado)
- **Ciclo**: `ClosedDate - CreatedDate` em dias
- **Status**: no prazo / atrasado / sem target date

## GitHub Pages + Releases

Tem dois workflows prontos em `.github/workflows/`:

- **`release.yml`**: builda binários pra Linux/Mac/Windows e publica num
  GitHub Release. Dispara ao criar uma tag `v*` (ex: `git tag v1.0.0 && git
  push --tags`) ou manualmente pela aba Actions.
- **`pages.yml`**: publica a pasta `docs/` (landing page com os links de
  download) no GitHub Pages. Dispara automaticamente quando `docs/` muda.

**Importante**: a landing page do GitHub Pages só mostra links de download —
ela **não roda a consulta** direto no navegador. O Azure DevOps bloqueia
chamadas de API vindas de outro domínio (CORS), então a ferramenta precisa
rodar localmente (o Go faz a chamada a partir da tua máquina, não do
navegador). Isso está explicado na própria landing page.

**Setup no repositório:**
1. Cria o repo no GitHub e sobe esse código
2. Em Settings → Pages, define a source como "GitHub Actions"
3. Cria uma tag (`git tag v1.0.0 && git push --tags`) pra gerar o primeiro release
4. A landing page detecta automaticamente teu usuário/repo pela URL e busca
   o último release via API do GitHub

## Build de binário único (opcional)

```bash
go build -o otd-tracker .
./otd-tracker
```

Gera um único executável (o front-end fica embutido via `embed.FS`).
