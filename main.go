package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type TaxRecord struct {
	Client string
	Date   string
	Charge float64
	Street string
	City   string
	State  string
	Zip    string
	Taxes  map[string]float64
}

type TaxRateResponse struct {
	TaxRates []struct {
		JurisName string `json:"JURISNAME"`
		JurisType string `json:"JURISTYPE"`
		JurisRate string `json:"JURISRATE"`
	} `json:"TAXRATES"`
	TotalTaxRate  string `json:"TOTALTAXRATE"`
	Street        string `json:"STREET"`
	City          string `json:"CITY"`
	ZipCode       string `json:"ZIPCODE"`
	GisReturnCode string `json:"GISRETURNCODE"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.SetOutput(os.Stdout)

	handler := http.HandlerFunc(taxRatesHandler)
	http.Handle("/getTaxRates", corsMiddleware(handler))

	log.Printf("Starting server on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "https://skeen0711.github.io")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func taxRatesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("csvFile")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	records, err := processCSV(file)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error processing CSV: %v", err), http.StatusInternalServerError)
		return
	}

	// Create a buffer for the ZIP file
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Write due_by_charge.csv
	dueByChargeBuf := new(bytes.Buffer)
	dueByChargeWriter := csv.NewWriter(dueByChargeBuf)
	jurisNames := getAllJurisNames(records)
	headers := []string{"client", "date", "charge", "street address", "city", "State", "zip code"}
	headers = append(headers, jurisNames...)
	if err := dueByChargeWriter.Write(headers); err != nil {
		http.Error(w, fmt.Sprintf("Error writing due_by_charge headers: %v", err), http.StatusInternalServerError)
		return
	}

	for _, rec := range records {
		row := []string{
			rec.Client,
			rec.Date,
			fmt.Sprintf("%.2f", rec.Charge),
			rec.Street,
			rec.City,
			rec.State,
			rec.Zip,
		}
		for _, juris := range jurisNames {
			tax := rec.Taxes[juris]
			row = append(row, fmt.Sprintf("%.2f", tax))
		}
		if err := dueByChargeWriter.Write(row); err != nil {
			http.Error(w, fmt.Sprintf("Error writing due_by_charge row: %v", err), http.StatusInternalServerError)
			return
		}
	}
	dueByChargeWriter.Flush()
	if err := dueByChargeWriter.Error(); err != nil {
		http.Error(w, fmt.Sprintf("Error flushing due_by_charge writer: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("due_by_charge.csv content length: %d bytes", dueByChargeBuf.Len())
	f1, err := zipWriter.Create("due_by_charge.csv")
	if err != nil {
		http.Error(w, fmt.Sprintf("Error creating due_by_charge.zip entry: %v", err), http.StatusInternalServerError)
		return
	}
	n1, err := io.Copy(f1, dueByChargeBuf)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error writing due_by_charge.csv to ZIP: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("due_by_charge.csv written to ZIP: %d bytes", n1)

	// Calculate totals per jurisdiction and write due_by_jurisdiction.csv
	jurisTotals := make(map[string]float64)
	for _, rec := range records {
		for juris, tax := range rec.Taxes {
			jurisTotals[juris] += tax
		}
	}

	dueByJurisBuf := new(bytes.Buffer)
	dueByJurisWriter := csv.NewWriter(dueByJurisBuf)
	if err := dueByJurisWriter.Write([]string{"Jurisdiction", "total"}); err != nil {
		http.Error(w, fmt.Sprintf("Error writing due_by_jurisdiction headers: %v", err), http.StatusInternalServerError)
		return
	}
	for juris, total := range jurisTotals {
		if err := dueByJurisWriter.Write([]string{juris, fmt.Sprintf("%.2f", total)}); err != nil {
			http.Error(w, fmt.Sprintf("Error writing due_by_jurisdiction row: %v", err), http.StatusInternalServerError)
			return
		}
	}
	dueByJurisWriter.Flush()
	if err := dueByJurisWriter.Error(); err != nil {
		http.Error(w, fmt.Sprintf("Error flushing due_by_jurisdiction writer: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("due_by_jurisdiction.csv content length: %d bytes", dueByJurisBuf.Len())
	f2, err := zipWriter.Create("due_by_jurisdiction.csv")
	if err != nil {
		http.Error(w, fmt.Sprintf("Error creating due_by_jurisdiction.zip entry: %v", err), http.StatusInternalServerError)
		return
	}
	n2, err := io.Copy(f2, dueByJurisBuf)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error writing due_by_jurisdiction.csv to ZIP: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("due_by_jurisdiction.csv written to ZIP: %d bytes", n2)

	// Explicitly close the ZIP writer before sending
	if err := zipWriter.Close(); err != nil {
		http.Error(w, fmt.Sprintf("Error closing ZIP writer: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Final ZIP file size: %d bytes", buf.Len())

	// Set response headers
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"tax_results.zip\"")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))

	// Write the ZIP buffer to the response
	n, err := w.Write(buf.Bytes())
	if err != nil {
		log.Printf("Error writing ZIP to response: %v", err)
		return
	}
	log.Printf("Wrote %d bytes to HTTP response", n)
}

func getAllJurisNames(records []TaxRecord) []string {
	jurisSet := make(map[string]bool)
	for _, rec := range records {
		for juris := range rec.Taxes {
			jurisSet[juris] = true
		}
	}
	var jurisNames []string
	for juris := range jurisSet {
		jurisNames = append(jurisNames, juris)
	}
	return jurisNames
}

func processCSV(file io.Reader) ([]TaxRecord, error) {
	reader := csv.NewReader(file)
	records := []TaxRecord{}

	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	expected := []string{"client", "date", "charge", "street address", "city", "State", "zip code"}
	if !equal(header, expected) {
		return nil, fmt.Errorf("invalid CSV header: got %v, expected %v", header, expected)
	}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}

		dateParts := strings.Split(row[1], "/")
		if len(dateParts) != 3 {
			return nil, fmt.Errorf("invalid date format for client %s: %s", row[0], row[1])
		}
		month, err := strconv.Atoi(dateParts[0])
		if err != nil || month < 1 || month > 12 {
			return nil, fmt.Errorf("invalid month in date for client %s: %s", row[0], row[1])
		}
		day, err := strconv.Atoi(dateParts[1])
		if err != nil || day < 1 || day > 31 {
			return nil, fmt.Errorf("invalid day in date for client %s: %s", row[0], row[1])
		}
		year, err := strconv.Atoi(dateParts[2])
		if err != nil || year < 2000 {
			return nil, fmt.Errorf("invalid year in date for client %s: %s", row[0], row[1])
		}

		quarter := (month-1)/3 + 1

		charge, err := strconv.ParseFloat(row[2], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid charge for client %s: %v", row[0], err)
		}

		rec := TaxRecord{
			Client: row[0],
			Date:   row[1],
			Charge: charge,
			Street: row[3],
			City:   row[4],
			State:  row[5],
			Zip:    row[6],
			Taxes:  make(map[string]float64),
		}

		taxRates, err := scrapeTaxRates(rec.Street, rec.City, rec.State, rec.Zip, quarter, year)
		if err != nil {
			return nil, fmt.Errorf("error scraping tax rates for %s: %v", rec.Client, err)
		}

		for juris, rate := range taxRates {
			rec.Taxes[juris] = charge * rate
		}

		records = append(records, rec)
	}

	return records, nil
}

func scrapeTaxRates(street, city, state, zip string, quarter, year int) (map[string]float64, error) {
	params := url.Values{
		"state":   {state},
		"city":    {city},
		"zipcode": {zip},
		"street":  {street},
		"quarter": {strconv.Itoa(quarter)},
		"year":    {strconv.Itoa(year)},
	}

	req, err := http.NewRequest("GET", "https://mulesoft.cpa.texas.gov:8088/api/cpa/gis/v1/salestaxrate/salestaxrate?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("client_id", "7cf772234a1744cfa78840c848e2d121")
	req.Header.Set("client_secret", "F00Fcb198e944A18A208EF7033C9B219")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tax rates: %v", err)
	}
	defer resp.Body.Close()

	log.Printf("API request URL: %s", req.URL.String())
	log.Printf("API response status: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}
	log.Printf("Raw API response: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d - %s", resp.StatusCode, string(body))
	}

	var taxData TaxRateResponse
	if err := json.Unmarshal(body, &taxData); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %v - raw response: %s", err, string(body))
	}

	taxRates := make(map[string]float64)
	for _, rate := range taxData.TaxRates {
		r, err := strconv.ParseFloat(rate.JurisRate, 64)
		if err != nil {
			log.Printf("Warning: Failed to parse rate %s for %s: %v", rate.JurisRate, rate.JurisName, err)
			continue
		}
		taxRates[rate.JurisName] = r
	}

	log.Printf("Parsed rates: %+v", taxRates)

	if len(taxRates) == 0 {
		return nil, fmt.Errorf("no tax rates found in response: %+v", taxData)
	}

	return taxRates, nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
