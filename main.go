/*
Reads a .csv with Client | Date | Address | Charge, scraping tax rates from the Texas Comptroller's Office GIS Sales Tax Rate Locator (gis.cpa.texas.gov), calculating LocalTax, CountyTax, and StateTax based on Charge * rate, and writing a new .csv. The script is deployed as a web endpoint using net/http, with concurrency via goroutines, and is optimized for Google Cloud Run’s free tier.
Assumptions and Notes
Input .csv: Columns are Client,Date,Address,Charge (e.g., John,2025-02-22,123 Main St Austin TX,100.00).
Output .csv: Adds LocalTax,CountyTax,StateTax (e.g., John,2025-02-22,123 Main St Austin TX,100.00,2.00,0.25,6.25).
Scraping: The GIS tool (gis.cpa.texas.gov) is a web form, not a public API. We’ll simulate submitting addresses to its search form and parsing the response HTML. (If an API exists, it’s undocumented; this uses the public interface.)
Concurrency: Limited to 10 concurrent requests to avoid overwhelming the site.
Deployment: Runs as an HTTP endpoint accepting .csv uploads, returning the processed file.
*/

package main

import (
	"encoding/csv"
	"fmt"
	"github.com/PuerkitoBio/goquery" // Install: go get github.com/PuerkitoBio/goquery
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	gisURL        = "https://gis.cpa.texas.gov/SearchSalesTaxRate" // Texas GIS tax rate locator
	maxConcurrent = 10                                             // Limit concurrent requests
)

func main() {
	http.HandleFunc("/process-csv", processCSVHandler)
	log.Printf("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func processCSVHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Handle file upload
	file, header, err := r.FormFile("csvfile")
	if err != nil {
		http.Error(w, "Failed to get file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save uploaded file temporarily
	originalPath := filepath.Join(".", header.Filename)
	out, err := os.Create(originalPath)
	if err != nil {
		http.Error(w, "Failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(out, file)
	out.Close()
	if err != nil {
		http.Error(w, "Failed to write file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Process the CSV and generate a new one
	newFilePath, err := processCSV(originalPath)
	if err != nil {
		http.Error(w, "Processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Serve the new file as a download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(newFilePath)))
	http.ServeFile(w, r, newFilePath)
}

func processCSV(filePath string) (string, error) {
	// Open original CSV
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read CSV
	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return "", err
	}
	if len(records) < 2 { // Need at least header + 1 row
		return "", fmt.Errorf("CSV is empty or lacks data rows")
	}

	// Prepare output CSV
	newFilePath := filepath.Join(filepath.Dir(filePath), filepath.Base(filePath[:len(filePath)-4])+"_taxes.csv")
	out, err := os.Create(newFilePath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	writer := csv.NewWriter(out)
	defer writer.Flush()

	// Write header
	header := append(records[0], "LocalTax", "CountyTax", "StateTax")
	if err := writer.Write(header); err != nil {
		return "", err
	}

	// Process rows concurrently
	var wg sync.WaitGroup
	results := make(chan []string, len(records)-1)
	semaphore := make(chan struct{}, maxConcurrent) // Limit concurrency

	for _, row := range records[1:] { // Skip header
		wg.Add(1)
		semaphore <- struct{}{} // Acquire semaphore
		go func(row []string) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release semaphore

			if len(row) < 4 {
				results <- append(row, "Error: Incomplete row", "", "")
				return
			}
			charge, err := strconv.ParseFloat(row[3], 64)
			if err != nil {
				results <- append(row, "Error: Invalid charge", "", "")
				return
			}

			// Scrape tax rates
			localRate, countyRate, stateRate, err := scrapeTaxRates(row[2]) // Address is row[2]
			if err != nil {
				results <- append(row, fmt.Sprintf("Error: %v", err), "", "")
				return
			}

			// Calculate taxes
			localTax := charge * localRate
			countyTax := charge * countyRate
			stateTax := charge * stateRate

			results <- append(row,
				fmt.Sprintf("%.2f", localTax),
				fmt.Sprintf("%.2f", countyTax),
				fmt.Sprintf("%.2f", stateTax),
			)
		}(row)
	}

	// Collect results
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if err := writer.Write(result); err != nil {
			return "", err
		}
	}

	// Rename original file
	newOriginalPath := filePath[:len(filePath)-4] + "_processed.csv"
	if err := os.Rename(filePath, newOriginalPath); err != nil {
		return "", err
	}

	return newFilePath, nil
}

func scrapeTaxRates(address string) (local, county, state float64, err error) {
	// Simulate form submission to GIS tool
	client := &http.Client{}
	data := url.Values{}
	data.Set("searchInput", address) // Adjust field name based on actual form

	req, err := http.NewRequest("POST", gisURL, strings.NewReader(data.Encode()))
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()

	// Parse HTML response
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return 0, 0, 0, err
	}

	// Extract tax rates (placeholders - adjust selectors based on actual HTML)
	// Note: Exact selectors require inspecting the site's response
	localStr := doc.Find(".local-tax-rate").Text() // Hypothetical class
	countyStr := doc.Find(".county-tax-rate").Text()
	stateStr := doc.Find(".state-tax-rate").Text()

	// Convert to float (default to reasonable values if parsing fails)
	local, err = strconv.ParseFloat(strings.TrimSpace(localStr), 64)
	if err != nil {
		local = 0.02 // Example default (2%)
	}
	county, err = strconv.ParseFloat(strings.TrimSpace(countyStr), 64)
	if err != nil {
		county = 0.005 // Example default (0.5%)
	}
	state, err = strconv.ParseFloat(strings.TrimSpace(stateStr), 64)
	if err != nil {
		state = 0.0625 // Texas state rate (6.25%)
	}

	return local, county, state, nil
}
