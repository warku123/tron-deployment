package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AddressRow is one (base58, hex, private-key) triple for the receivers
// sidecar.
type AddressRow struct {
	Base58     string
	HexAddress string
	PrivateKey string
}

// WriteAddressList writes rows to path in CSV form with a header.
func WriteAddressList(path string, rows []AddressRow) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"base58", "hex_address", "private_key"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{r.Base58, r.HexAddress, r.PrivateKey}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// ListGeneratedTxFiles returns every generate-tx*.csv in dir, sorted.
func ListGeneratedTxFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "generate-tx") && strings.HasSuffix(name, ".csv") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no generate-tx*.csv files in %s", dir)
	}
	return files, nil
}
