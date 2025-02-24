package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type TaxRecord struct {
	Client    string
	Charge    float64
	Street    string
	City      string
	State     string
	Zip       string
	CityTax   float64
	CountyTax float64
	StateTax  float64
}

// Assume this is the JSON structure (adjust after seeing real response)
type TaxRateResponse struct {
	Rates []struct {
		Jurisdiction string  `json:"jurisdiction"`
		Type         string  `json:"type"`
		Rate         float64 `json:"rate"`
	} `json:"rates"`
	TotalRate float64 `json:"total_rate"`
}

func main() {
	http.HandleFunc("/taxScraper", taxScraperHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func taxScraperHandler(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "Error processing CSV: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=output.csv")
	w.Header().Set("Content-Type", "text/csv")
	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"client", "charge", "street address", "city", "State", "zip code", "city tax", "county tax", "state tax"})
	for _, rec := range records {
		writer.Write([]string{
			rec.Client,
			fmt.Sprintf("%.2f", rec.Charge),
			rec.Street,
			rec.City,
			rec.State,
			rec.Zip,
			fmt.Sprintf("%.2f", rec.CityTax),
			fmt.Sprintf("%.2f", rec.CountyTax),
			fmt.Sprintf("%.2f", rec.StateTax),
		})
	}
}

func processCSV(file io.Reader) ([]TaxRecord, error) {
	reader := csv.NewReader(file)
	records := []TaxRecord{}

	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	expected := []string{"client", "charge", "street address", "city", "State", "zip code"}
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

		charge, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid charge for client %s: %v", row[0], err)
		}

		rec := TaxRecord{
			Client: row[0],
			Charge: charge,
			Street: row[2],
			City:   row[3],
			State:  row[4],
			Zip:    row[5],
		}

		cityRate, countyRate, stateRate, err := scrapeTaxRates(rec.Street, rec.City, rec.State, rec.Zip)
		if err != nil {
			return nil, fmt.Errorf("error scraping tax rates for %s: %v", rec.Client, err)
		}

		rec.CityTax = charge * cityRate
		rec.CountyTax = charge * countyRate
		rec.StateTax = charge * stateRate

		records = append(records, rec)
	}

	return records, nil
}

func scrapeTaxRates(street, city, state, zip string) (float64, float64, float64, error) {
	// Build query parameters
	params := url.Values{
		"state":   {state},
		"city":    {city},
		"zipcode": {zip},
		"street":  {street},
		"quarter": {"1"},                             // Hardcoded for now
		"year":    {strconv.Itoa(time.Now().Year())}, // Current year (2025 as of Feb 23, 2025)
	}

	// Create request
	req, err := http.NewRequest("GET", "https://mulesoft.cpa.texas.gov:8088/api/cpa/gis/v1/salestaxrate/salestaxrate?"+params.Encode(), nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to create request: %v", err)
	}

	// Add required headers
	req.Header.Set("client_id", "7cf772234a1744cfa78840c848e2d121")
	req.Header.Set("client_secret", "F00Fcb198e944A18A208EF7033C9B219")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)")

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to fetch tax rates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Parse JSON response
	var taxData TaxRateResponse
	if err := json.NewDecoder(resp.Body).Decode(&taxData); err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse JSON: %v", err)
	}

	// Extract rates
	var cityRate, countyRate, stateRate float64
	for _, rate := range taxData.Rates {
		switch rate.Type {
		case "STATE":
			stateRate = rate.Rate
		case "COUNTY":
			countyRate = rate.Rate
		case "CITY":
			cityRate = rate.Rate
		}
	}

	if stateRate == 0 {
		return 0, 0, 0, fmt.Errorf("no state tax rate found")
	}

	return cityRate, countyRate, stateRate, nil
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
