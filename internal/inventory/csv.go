package inventory

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadCSV imports legacy two-column CSV records into validated inventory tags.
// Column one is retained only for source compatibility. Column two is binary EPC.
func LoadCSV(path string, limits Limits) ([]Tag, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(io.LimitReader(file, limits.MaxInputBytes+1))
	seen := make(map[string]struct{})
	tags := make([]Tag, 0)
	for line := 1; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("inventory: CSV row %d: %w", line, err)
		}
		if len(tags) == limits.MaxRecords {
			return nil, ErrTooManyTags
		}
		if len(record) != 2 {
			return nil, fmt.Errorf("inventory: CSV row %d requires two fields", line)
		}
		epc, err := parseBinaryEPC(record[1])
		if err != nil {
			return nil, fmt.Errorf("inventory: CSV row %d: %w", line, err)
		}
		tag, err := NewTag(epc)
		if err != nil {
			return nil, err
		}
		if len(epc) > limits.MaxEPCBytes {
			return nil, ErrInputTooLarge
		}
		if _, exists := seen[tag.key()]; exists {
			return nil, fmt.Errorf("inventory: duplicate EPC %s", tag.Hex())
		}
		seen[tag.key()] = struct{}{}
		tags = append(tags, tag)
	}
	return tags, nil
}

func parseBinaryEPC(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if len(value)%8 != 0 {
		return nil, ErrInvalidEPC
	}
	epc := make([]byte, len(value)/8)
	for index, bit := range value {
		if bit != '0' && bit != '1' {
			return nil, ErrInvalidEPC
		}
		if bit == '1' {
			epc[index/8] |= 1 << (7 - index%8)
		}
	}
	return epc, nil
}
