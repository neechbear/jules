package xnb_parse

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRandomXNBFiles(t *testing.T) {
	xnbFileDirs := make(map[string][]string)

	err := filepath.Walk("../Content", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".xnb" {
			file, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer file.Close()

			header, err := ParseHeader(file)
			if err != nil {
				return nil
			}

			isCompressed := (header.Flags & 0x80) != 0
			if !isCompressed {
				dir := filepath.Dir(path)
				xnbFileDirs[dir] = append(xnbFileDirs[dir], path)
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Error walking the Content directory: %v", err)
	}

	if len(xnbFileDirs) == 0 {
		t.Fatal("No uncompressed XNB files found in the Content directory.")
	}

	for _, files := range xnbFileDirs {
		filePath := files[rand.Intn(len(files))]
		t.Run(fmt.Sprintf("Parsing %s", filePath), func(t *testing.T) {
			file, err := os.Open(filePath)
			if err != nil {
				t.Fatalf("Error opening file %s: %v", filePath, err)
			}
			defer file.Close()

			_, err = Parse(file)
			if err != nil {
				t.Errorf("Error parsing XNB file %s: %v", filePath, err)
			}
		})
	}
}
