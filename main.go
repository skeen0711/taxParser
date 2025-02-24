package main

import (
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

type TaxRateResponse struct {
	TaxRates []struct {
		JurisName string `json:"JURISNAME"`
		JurisType string `json:"JURISTYPE"`
		JurisRate string `json:"JURISRATE"`
	} `json:"TAXRATES"`
	TotalTaxRate  string `json:"TOTALTAXRATE"`
	Street        string `json:"STREET"` // To verify address match
	City          string `json:"CITY"`
	ZipCode       string `json:"ZIPCODE"`
	GisReturnCode string `json:"GISRETURNCODE"` // "P" for precise match, might indicate no match
}

func main() {
	logFile, err := os.Create("debug.log")
	if err != nil {
		log.Fatalf("Error creating log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	inputFile, err := os.Open("taxFinderTest1.csv")
	if err != nil {
		log.Fatalf("Error opening input CSV: %v", err)
	}
	defer inputFile.Close()

	records, err := processCSV(inputFile)
	if err != nil {
		log.Fatalf("Error processing CSV: %v", err)
	}

	outputFile, err := os.Create("output.csv")
	if err != nil {
		log.Fatalf("Error creating output CSV: %v", err)
	}
	defer outputFile.Close()

	writer := csv.NewWriter(outputFile)
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

	fmt.Println("Processing complete. Output written to output.csv")
}

func processCSV(file *os.File) ([]TaxRecord, error) {
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

		// Strip whitespace from all fields
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
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
	params := url.Values{
		"state":   {state},
		"city":    {city},
		"zipcode": {zip},
		"street":  {street},
		"quarter": {"1"},
		"year":    {strconv.Itoa(time.Now().Year())},
	}

	req, err := http.NewRequest("GET", "https://mulesoft.cpa.texas.gov:8088/api/cpa/gis/v1/salestaxrate/salestaxrate?"+params.Encode(), nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("client_id", "7cf772234a1744cfa78840c848e2d121")
	req.Header.Set("client_secret", "F00Fcb198e944A18A208EF7033C9B219")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to fetch tax rates: %v", err)
	}
	defer resp.Body.Close()

	log.Printf("API request URL: %s", req.URL.String())
	log.Printf("API response status: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to read response body: %v", err)
	}
	log.Printf("Raw API response: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, fmt.Errorf("unexpected status code: %d - %s", resp.StatusCode, string(body))
	}

	var taxData TaxRateResponse
	if err := json.Unmarshal(body, &taxData); err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse JSON: %v - raw response: %s", err, string(body))
	}

	// Check if address matches input (case-insensitive)
	inputStreet := strings.ToUpper(street)
	inputCity := strings.ToUpper(city)
	inputZip := zip
	returnedStreet := strings.ToUpper(taxData.Street)
	returnedCity := strings.ToUpper(taxData.City)
	returnedZip := taxData.ZipCode

	if inputStreet != returnedStreet || inputCity != returnedCity || inputZip != returnedZip {
		log.Printf("Warning: Address mismatch - Input: %s, %s, %s; Returned: %s, %s, %s",
			inputStreet, inputCity, inputZip, returnedStreet, returnedCity, returnedZip)
	}

	var cityRate, countyRate, stateRate float64
	for _, rate := range taxData.TaxRates {
		r, err := strconv.ParseFloat(rate.JurisRate, 64)
		if err != nil {
			log.Printf("Warning: Failed to parse rate %s for %s: %v", rate.JurisRate, rate.JurisType, err)
			continue
		}
		switch rate.JurisType {
		case "STATE":
			stateRate = r
		case "COUNTY":
			countyRate = r
		case "CITY":
			cityRate = r
		}
	}

	log.Printf("Parsed rates - City: %f, County: %f, State: %f", cityRate, countyRate, stateRate)

	if stateRate == 0 {
		return 0, 0, 0, fmt.Errorf("no state tax rate found in response: %+v", taxData)
	}

	// Log if fewer than expected rates are found
	if len(taxData.TaxRates) < 3 {
		log.Printf("Warning: Only %d tax rates found (expected 3: CITY, COUNTY, STATE)", len(taxData.TaxRates))
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
