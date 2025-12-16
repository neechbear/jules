package main

import (
	"encoding/json"
	"fmt"
	"image"
	"encoding/binary"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/Jules-Engineering/xnb_parse"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: xnb_parse_cli <file.xnb>")
		os.Exit(1)
	}

	filePath := os.Args[1]
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file:", err)
		os.Exit(1)
	}
	defer file.Close()

	asset, err := xnb_parse.Parse(file)
	if err != nil {
		fmt.Println("Error parsing XNB file:", err)
		os.Exit(1)
	}

	switch v := asset.(type) {
	case xnb_parse.Texture2D:
		img, err := decodeTexture(v)
		if err != nil {
			fmt.Println("Error decoding texture:", err)
			os.Exit(1)
		}
		savePNG(filePath, img)
	case xnb_parse.SoundEffect:
		saveWAV(filePath, v)
	case xnb_parse.SpriteFont:
		img, err := decodeTexture(v.Texture.(xnb_parse.Texture2D))
		if err != nil {
			fmt.Println("Error decoding texture:", err)
			os.Exit(1)
		}
		savePNG(filePath, img)
		saveFontData(filePath, v)
	default:
		fmt.Printf("Successfully parsed XNB file. Asset type: %T\n", asset)
	}
}

func decodeTexture(texture xnb_parse.Texture2D) (image.Image, error) {
	if len(texture.MipmapLevels) == 0 {
		return nil, fmt.Errorf("no mipmap levels found")
	}

	pixelData := texture.MipmapLevels[0]
	img := image.NewRGBA(image.Rect(0, 0, int(texture.Width), int(texture.Height)))

	switch texture.Format {
	case xnb_parse.SurfaceFormatColor:
		for i := 0; i < len(pixelData); i += 4 {
			img.Set(
				(i/4)%int(texture.Width),
				(i/4)/int(texture.Width),
				color.RGBA{R: pixelData[i+2], G: pixelData[i+1], B: pixelData[i], A: pixelData[i+3]},
			)
		}
	default:
		return nil, fmt.Errorf("unsupported surface format: %d", texture.Format)
	}

	return img, nil
}

func savePNG(filePath string, img image.Image) {
	outPath := filepath.Base(filePath) + ".png"
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		os.Exit(1)
	}
	defer outFile.Close()

	err = png.Encode(outFile, img)
	if err != nil {
		fmt.Println("Error encoding PNG:", err)
		os.Exit(1)
	}
	fmt.Println("Saved texture to", outPath)
}

func saveWAV(filePath string, sound xnb_parse.SoundEffect) {
	outPath := filepath.Base(filePath) + ".wav"
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		os.Exit(1)
	}
	defer outFile.Close()

	// Write WAV header
	// This is a simplified header and may not work for all formats.
	header := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00")
	outFile.Write(header)
	outFile.Write(sound.Format)
	dataHeader := []byte("data\x00\x00\x00\x00")
	binary.LittleEndian.PutUint32(dataHeader[4:], uint32(len(sound.Data)))
	outFile.Write(dataHeader)
	outFile.Write(sound.Data)

	// Update file size in header
	size := uint32(len(header) + len(sound.Format) + len(dataHeader) + len(sound.Data) - 8)
	outFile.Seek(4, 0)
	binary.Write(outFile, binary.LittleEndian, size)

	fmt.Println("Saved sound to", outPath)
}

func saveFontData(filePath string, font xnb_parse.SpriteFont) {
	outPath := filepath.Base(filePath) + ".json"
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		os.Exit(1)
	}
	defer outFile.Close()

	encoder := json.NewEncoder(outFile)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(font)
	if err != nil {
		fmt.Println("Error encoding JSON:", err)
		os.Exit(1)
	}
	fmt.Println("Saved font data to", outPath)
}
