package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// ---------- Tipos de request/response ----------

type QueryRequest struct {
	Org         string `json:"org"`         // ex: "db1group"
	Project     string `json:"project"`     // ex: "WinnerBox"
	PAT         string `json:"pat"`         // Personal Access Token
	AssignedTo  string `json:"assignedTo"`  // ex: "Iago Holek <iago.holek@db1.com.br>"
	DateFrom    string `json:"dateFrom"`    // YYYY-MM-DD
	DateTo      string `json:"dateTo"`      // YYYY-MM-DD
	EntregaFlag bool   `json:"entregaFlag"` // exige Custom.ENTREGA_DE_VALOR = 'SIM'
}

type WiqlRequestBody struct {
	Query string `json:"query"`
}

type WiqlResponse struct {
	WorkItemRelations []struct {
		Rel    string `json:"rel"`
		Source *struct {
			ID int `json:"id"`
		} `json:"source"`
		Target *struct {
			ID int `json:"id"`
		} `json:"target"`
	} `json:"workItemRelations"`
}

type WorkItemsBatchRequest struct {
	IDs    []int    `json:"ids"`
	Fields []string `json:"fields"`
}

type WorkItemFields struct {
	Title      string `json:"System.Title"`
	State      string `json:"System.State"`
	Type       string `json:"System.WorkItemType"`
	AssignedTo *struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"System.AssignedTo"`
	CreatedDate string `json:"System.CreatedDate"`
	TargetDate  string `json:"Microsoft.VSTS.Scheduling.TargetDate"`
	ClosedDate  string `json:"Microsoft.VSTS.Common.ClosedDate"`
}

type WorkItem struct {
	ID     int            `json:"id"`
	Fields WorkItemFields `json:"fields"`
}

type WorkItemsBatchResponse struct {
	Value []WorkItem `json:"value"`
}

type PBIResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	AssignedTo    string `json:"assignedTo"`
	CreatedDate   string `json:"createdDate"`
	TargetDate    string `json:"targetDate"`
	ClosedDate    string `json:"closedDate"`
	OTDStatus     string `json:"otdStatus"` // "no_prazo" | "atrasado" | "sem_target"
	DriftDays     int    `json:"driftDays"` // closed - target (positivo = atrasado)
	CycleDays     int    `json:"cycleDays"` // closed - created
	HasTargetDate bool   `json:"hasTargetDate"`
}

type MembersRequest struct {
	Org     string `json:"org"`
	Project string `json:"project"`
	PAT     string `json:"pat"`
}

type WiqlFlatResponse struct {
	WorkItems []struct {
		ID int `json:"id"`
	} `json:"workItems"`
}

type Member struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
	Filter      string `json:"filter"` // formato "Nome <email>" pra usar direto no WIQL
}

// ---------- Handler principal ----------

func handleQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "corpo da requisição inválido: "+err.Error())
		return
	}
	if req.Org == "" || req.Project == "" || req.PAT == "" || req.AssignedTo == "" {
		writeErr(w, "org, project, pat e assignedTo são obrigatórios")
		return
	}

	wiql := buildWiql(req)

	sourceIDs, err := runWiql(req.Org, req.Project, req.PAT, wiql)
	if err != nil {
		writeErr(w, "erro consultando WIQL: "+err.Error())
		return
	}

	if len(sourceIDs) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"items": []PBIResult{}})
		return
	}

	items, err := fetchWorkItems(req.Org, req.PAT, sourceIDs)
	if err != nil {
		writeErr(w, "erro buscando work items: "+err.Error())
		return
	}

	results := buildResults(items)

	json.NewEncoder(w).Encode(map[string]interface{}{"items": results})
}

func writeErr(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handleMembers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req MembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "corpo da requisição inválido: "+err.Error())
		return
	}
	if req.Org == "" || req.Project == "" || req.PAT == "" {
		writeErr(w, "org, project e pat são obrigatórios")
		return
	}

	ids, err := runMembersWiql(req.Org, req.Project, req.PAT)
	if err != nil {
		writeErr(w, "erro consultando work items: "+err.Error())
		return
	}
	if len(ids) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"members": []Member{}})
		return
	}

	// workitemsbatch aceita até 200 ids por chamada
	if len(ids) > 200 {
		ids = ids[:200]
	}

	items, err := fetchAssignedToOnly(req.Org, req.PAT, ids)
	if err != nil {
		writeErr(w, "erro buscando assigned to: "+err.Error())
		return
	}

	seen := map[string]bool{}
	var members []Member
	for _, wi := range items {
		if wi.Fields.AssignedTo == nil {
			continue
		}
		a := wi.Fields.AssignedTo
		if a.UniqueName == "" || seen[a.UniqueName] {
			continue
		}
		seen[a.UniqueName] = true
		members = append(members, Member{
			DisplayName: a.DisplayName,
			UniqueName:  a.UniqueName,
			Filter:      fmt.Sprintf("%s <%s>", a.DisplayName, a.UniqueName),
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"members": members})
}

func runMembersWiql(org, project, pat string) ([]int, error) {
	wiql := fmt.Sprintf(`SELECT [System.Id]
FROM WorkItems
WHERE [System.TeamProject] = '%s'
    AND [System.WorkItemType] IN ('Development', 'Test Execution')
    AND [System.ChangedDate] >= @today - 180
ORDER BY [System.ChangedDate] DESC`, esc(project))

	url := fmt.Sprintf("https://dev.azure.com/%s/%s/_apis/wit/wiql?api-version=7.1", org, project)
	body, _ := json.Marshal(WiqlRequestBody{Query: wiql})

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(pat))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d ao chamar WIQL: %s", resp.StatusCode, string(respBody))
	}

	var flatResp WiqlFlatResponse
	if err := json.Unmarshal(respBody, &flatResp); err != nil {
		return nil, fmt.Errorf("erro parseando resposta WIQL: %w", err)
	}

	ids := make([]int, 0, len(flatResp.WorkItems))
	for _, wi := range flatResp.WorkItems {
		ids = append(ids, wi.ID)
	}
	return ids, nil
}

func fetchAssignedToOnly(org, pat string, ids []int) ([]WorkItem, error) {
	url := fmt.Sprintf("https://dev.azure.com/%s/_apis/wit/workitemsbatch?api-version=7.1", org)

	reqBody, _ := json.Marshal(WorkItemsBatchRequest{IDs: ids, Fields: []string{"System.Id", "System.AssignedTo"}})

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(pat))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d ao chamar workitemsbatch: %s", resp.StatusCode, string(respBody))
	}

	var batchResp WorkItemsBatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return nil, fmt.Errorf("erro parseando workitemsbatch: %w", err)
	}
	return batchResp.Value, nil
}

// ---------- Montagem da query WIQL ----------

func esc(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func buildWiql(req QueryRequest) string {
	entregaClause := ""
	if req.EntregaFlag {
		entregaClause = "\n        AND [Source].[Custom.ENTREGA_DE_VALOR] = 'SIM'"
	}

	return fmt.Sprintf(`SELECT
    [System.Id],
    [System.WorkItemType],
    [System.Title],
    [System.AssignedTo],
    [System.State],
    [Microsoft.VSTS.Scheduling.TargetDate],
    [Microsoft.VSTS.Common.ClosedDate]
FROM workitemLinks
WHERE
    (
        [Source].[System.TeamProject] = '%s'
        AND [Source].[System.WorkItemType] = 'PBI'
        AND [Source].[System.State] = 'Closed'
        AND [Source].[Microsoft.VSTS.Common.ClosedDate] >= '%sT00:00:00.0000000'
        AND [Source].[Microsoft.VSTS.Common.ClosedDate] <= '%sT00:00:00.0000000'%s
    )
    AND (
        [Target].[System.TeamProject] = '%s'
        AND [Target].[System.WorkItemType] IN ('Development', 'Code Review', 'Test Execution')
        AND [Target].[System.AssignedTo] = '%s'
        AND [Target].[System.State] = 'Closed'
    )
ORDER BY [Custom.OTD_AnyTools],
    [System.Id]
MODE (MustContain)`,
		esc(req.Project), esc(req.DateFrom), esc(req.DateTo), entregaClause,
		esc(req.Project), esc(req.AssignedTo))
}

// ---------- Chamadas à API do Azure DevOps ----------

func authHeader(pat string) string {
	token := base64.StdEncoding.EncodeToString([]byte(":" + pat))
	return "Basic " + token
}

func runWiql(org, project, pat, wiql string) ([]int, error) {
	url := fmt.Sprintf("https://dev.azure.com/%s/%s/_apis/wit/wiql?api-version=7.1", org, project)

	body, _ := json.Marshal(WiqlRequestBody{Query: wiql})

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(pat))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d ao chamar WIQL: %s", resp.StatusCode, string(respBody))
	}

	var wiqlResp WiqlResponse
	if err := json.Unmarshal(respBody, &wiqlResp); err != nil {
		return nil, fmt.Errorf("erro parseando resposta WIQL: %w", err)
	}

	seen := map[int]bool{}
	var ids []int
	for _, rel := range wiqlResp.WorkItemRelations {
		if rel.Source != nil && !seen[rel.Source.ID] {
			seen[rel.Source.ID] = true
			ids = append(ids, rel.Source.ID)
		}
	}
	return ids, nil
}

func fetchWorkItems(org, pat string, ids []int) ([]WorkItem, error) {
	url := fmt.Sprintf("https://dev.azure.com/%s/_apis/wit/workitemsbatch?api-version=7.1", org)

	fields := []string{
		"System.Id",
		"System.Title",
		"System.State",
		"System.WorkItemType",
		"System.AssignedTo",
		"System.CreatedDate",
		"Microsoft.VSTS.Scheduling.TargetDate",
		"Microsoft.VSTS.Common.ClosedDate",
	}

	reqBody, _ := json.Marshal(WorkItemsBatchRequest{IDs: ids, Fields: fields})

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(pat))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d ao chamar workitemsbatch: %s", resp.StatusCode, string(respBody))
	}

	var batchResp WorkItemsBatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return nil, fmt.Errorf("erro parseando workitemsbatch: %w", err)
	}

	return batchResp.Value, nil
}

// ---------- Cálculo de OTD ----------

const isoLayout = "2006-01-02T15:04:05Z"
const isoLayoutFrac = "2006-01-02T15:04:05.999999999Z"

func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(isoLayoutFrac, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(isoLayout, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func buildResults(items []WorkItem) []PBIResult {
	results := make([]PBIResult, 0, len(items))

	for _, wi := range items {
		f := wi.Fields
		assigned := ""
		if f.AssignedTo != nil {
			assigned = f.AssignedTo.DisplayName
		}

		res := PBIResult{
			ID:          wi.ID,
			Title:       f.Title,
			AssignedTo:  assigned,
			CreatedDate: f.CreatedDate,
			TargetDate:  f.TargetDate,
			ClosedDate:  f.ClosedDate,
		}

		created, hasCreated := parseDate(f.CreatedDate)
		target, hasTarget := parseDate(f.TargetDate)
		closed, hasClosed := parseDate(f.ClosedDate)

		res.HasTargetDate = hasTarget

		if hasCreated && hasClosed {
			res.CycleDays = int(closed.Sub(created).Hours() / 24)
		}

		if !hasTarget {
			res.OTDStatus = "sem_target"
		} else if hasClosed {
			// Compara só o DIA calendário (UTC), ignorando a hora. Um item
			// com Target 10/07 00:00 e ClosedDate 10/07 09:32 é "no prazo" —
			// comparar o timestamp completo marcaria isso como atrasado por
			// causa da hora, o que está errado.
			targetDay := truncateToDay(target)
			closedDay := truncateToDay(closed)
			drift := int(closedDay.Sub(targetDay).Hours() / 24)
			res.DriftDays = drift
			if closedDay.After(targetDay) {
				res.OTDStatus = "atrasado"
			} else {
				res.OTDStatus = "no_prazo"
			}
		} else {
			res.OTDStatus = "sem_target"
		}

		results = append(results, res)
	}

	return results
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ---------- main ----------

func main() {
	sub, err := staticSubFS()
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/api/query", handleQuery)
	http.HandleFunc("/api/members", handleMembers)

	port := "8080"
	fmt.Printf("Servidor rodando em http://localhost:%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
