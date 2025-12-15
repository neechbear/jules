package xnb_parse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// XNBHeader represents the header of an XNB file.
type XNBHeader struct {
	Magic    [3]byte
	Platform byte
	Version  byte
	Flags    byte
	FileSize uint32
}

// ContentTypeReader is an interface for reading a specific type from an XNB file.
type ContentTypeReader interface {
	Read(reader *ContentReader) (interface{}, error)
}

// ContentReader is a binary reader for XNB files.
type ContentReader struct {
	reader      io.Reader
	bo          binary.ByteOrder
	Platform    byte
	Version     byte
	typeReaders []ContentTypeReader
}

var typeReaderRegistry = make(map[string]func() ContentTypeReader)

// RegisterTypeReader registers a ContentTypeReader.
func RegisterTypeReader(typeName string, factory func() ContentTypeReader) {
	typeReaderRegistry[typeName] = factory
}

// NewContentReader creates a new ContentReader.
func NewContentReader(r io.Reader, platform byte, version byte) *ContentReader {
	return &ContentReader{
		reader:   r,
		bo:       binary.LittleEndian,
		Platform: platform,
		Version:  version,
	}
}

// Read7BitEncodedInt reads a 7-bit encoded integer from the stream.
func (cr *ContentReader) Read7BitEncodedInt() (int32, error) {
	var result int32
	var shift uint
	for i := 0; i < 5; i++ {
		var b byte
		err := binary.Read(cr.reader, cr.bo, &b)
		if err != nil {
			return 0, err
		}
		result |= int32(b&0x7f) << shift
		if (b & 0x80) == 0 {
			return result, nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("invalid 7-bit encoded integer")
}

// ReadString reads a string from the stream.
func (cr *ContentReader) ReadString() (string, error) {
	length, err := cr.Read7BitEncodedInt()
	if err != nil {
		return "", err
	}
	buf := make([]byte, length)
	_, err = io.ReadFull(cr.reader, buf)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// ParseHeader reads and validates the XNB header.
func ParseHeader(r io.Reader) (*XNBHeader, error) {
	var header XNBHeader
	err := binary.Read(r, binary.LittleEndian, &header)
	if err != nil {
		return nil, err
	}
	if header.Magic[0] != 'X' || header.Magic[1] != 'N' || header.Magic[2] != 'B' {
		return nil, fmt.Errorf("invalid XNB magic")
	}
	return &header, nil
}

// Parse is the main entry point for parsing an XNB file.
func Parse(r io.Reader) (interface{}, error) {
	header, err := ParseHeader(r)
	if err != nil {
		return nil, err
	}

	isCompressed := (header.Flags & 0x80) != 0
	var decompressedSize uint32
	if isCompressed {
		err := binary.Read(r, binary.LittleEndian, &decompressedSize)
		if err != nil {
			return nil, fmt.Errorf("failed to read decompressed size: %w", err)
		}
	}

	compressedSize := header.FileSize - 10
	if isCompressed {
		compressedSize -= 4 // for the decompressed size
	}

	var contentReader io.Reader
	if isCompressed {
		decoder := NewLzxDecoder(16)
		var decompressedData bytes.Buffer
		err = decoder.Decompress(r, int(compressedSize), &decompressedData, int(decompressedSize))
		if err != nil {
			return nil, err
		}
		contentReader = &decompressedData
	} else {
		contentReader = r
	}

	cr := NewContentReader(contentReader, header.Platform, header.Version)

	err = cr.InitializeTypeReaders()
	if err != nil {
		return nil, err
	}

	asset, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}

	return asset, nil
}

// InitializeTypeReaders reads the type reader information from the stream.
func (cr *ContentReader) InitializeTypeReaders() error {
	numTypeReaders, err := cr.Read7BitEncodedInt()
	if err != nil {
		return err
	}
	cr.typeReaders = make([]ContentTypeReader, numTypeReaders)
	typeReaderMap := make(map[string]ContentTypeReader)

	for i := 0; i < int(numTypeReaders); i++ {
		readerTypeString, err := cr.ReadString()
		if err != nil {
			return err
		}

		preparedTypeString := prepareType(readerTypeString)
		baseTypeName, genericArgs := parseGenericType(preparedTypeString)

		var newReader ContentTypeReader
		if strings.HasPrefix(baseTypeName, "Microsoft.Xna.Framework.Content.ListReader") {
			if len(genericArgs) != 1 {
				return fmt.Errorf("ListReader expects 1 generic argument, got %d", len(genericArgs))
			}
			elementTypeName := genericArgs[0]
			elementTypeReader, ok := typeReaderMap[elementTypeName]
			if !ok {
				baseElementTypeName, _ := parseGenericType(elementTypeName)
				elementTypeReader, ok = typeReaderMap[baseElementTypeName]
				if !ok {
					return fmt.Errorf("element type reader '%s' for ListReader not found in map", elementTypeName)
				}
			}
			newReader = &ListReader{ElementTypeReader: elementTypeReader}
		} else {
			factory, ok := typeReaderRegistry[baseTypeName]
			if !ok {
				return fmt.Errorf("unrecognized type reader: %s (%s)", readerTypeString, baseTypeName)
			}
			newReader = factory()
		}

		cr.typeReaders[i] = newReader
		typeReaderMap[baseTypeName] = newReader
		if len(genericArgs) > 0 {
			typeReaderMap[preparedTypeString] = newReader
		}

		var readerVersion int32
		err = binary.Read(cr.reader, cr.bo, &readerVersion)
		if err != nil {
			return err
		}
	}
	return nil
}

func parseGenericType(typeString string) (string, []string) {
	start := strings.Index(typeString, "[[")
	if start == -1 {
		return typeString, nil
	}
	end := strings.LastIndex(typeString, "]]")
	if end == -1 {
		return typeString, nil
	}
	baseType := typeString[:start]
	genericPart := typeString[start+2 : end]
	return baseType, strings.Split(genericPart, "],[")
}

var versionRegex = regexp.MustCompile(", Version=[^,]+, Culture=[^,]+, PublicKeyToken=[^,]+")

func prepareType(typeString string) string {
	typeString = versionRegex.ReplaceAllString(typeString, "")
	count := strings.Count(typeString, "[[")
	for i := 0; i < count; i++ {
		typeString = regexp.MustCompile(`\[([^\[\]]+?),[^\]]+?\]`).ReplaceAllString(typeString, "[$1]")
	}
	if strings.Contains(typeString, "PublicKeyToken") {
		typeString = regexp.MustCompile(`(.+?),[^\]]+?$`).ReplaceAllString(typeString, "$1")
	}
	return typeString
}

// ReadObject reads an object from the stream.
func (cr *ContentReader) ReadObject() (interface{}, error) {
	typeReaderIndex, err := cr.Read7BitEncodedInt()
	if err != nil {
		return nil, err
	}
	if typeReaderIndex == 0 {
		return nil, nil // null object
	}
	if typeReaderIndex > int32(len(cr.typeReaders)) {
		return nil, fmt.Errorf("type reader index out of bounds")
	}
	typeReader := cr.typeReaders[typeReaderIndex-1]
	return typeReader.Read(cr)
}

// StringReader reads a string.
type StringReader struct{}

func (r *StringReader) Read(cr *ContentReader) (interface{}, error) {
	return cr.ReadString()
}

// Int32Reader reads a 32-bit integer.
type Int32Reader struct{}

func (r *Int32Reader) Read(cr *ContentReader) (interface{}, error) {
	var val int32
	err := binary.Read(cr.reader, cr.bo, &val)
	return val, err
}

// BooleanReader reads a boolean.
type BooleanReader struct{}

func (r *BooleanReader) Read(cr *ContentReader) (interface{}, error) {
	var val bool
	err := binary.Read(cr.reader, cr.bo, &val)
	return val, err
}

// Vector2 is a 2D vector.
type Vector2 struct {
	X, Y float32
}

// Vector2Reader reads a Vector2.
type Vector2Reader struct{}

func (r *Vector2Reader) Read(cr *ContentReader) (interface{}, error) {
	var vec Vector2
	err := binary.Read(cr.reader, cr.bo, &vec)
	return vec, err
}

// ListReader reads a list of objects.
type ListReader struct {
	ElementTypeReader ContentTypeReader
}

func (r *ListReader) Read(cr *ContentReader) (interface{}, error) {
	count, err := cr.Read7BitEncodedInt()
	if err != nil {
		return nil, err
	}
	list := make([]interface{}, count)
	for i := 0; i < int(count); i++ {
		list[i], err = cr.ReadObject()
		if err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Effect represents a compiled shader.
type Effect struct {
	Bytecode []byte
}

// EffectReader reads an Effect.
type EffectReader struct{}

func (r *EffectReader) Read(cr *ContentReader) (interface{}, error) {
	var length int32
	err := binary.Read(cr.reader, cr.bo, &length)
	if err != nil {
		return nil, err
	}
	bytecode := make([]byte, length)
	_, err = io.ReadFull(cr.reader, bytecode)
	if err != nil {
		return nil, err
	}
	return Effect{Bytecode: bytecode}, nil
}

// ReflectiveReader is a placeholder for a reader that uses reflection.
type ReflectiveReader struct{}

func (r *ReflectiveReader) Read(cr *ContentReader) (interface{}, error) {
	return nil, fmt.Errorf("ReflectiveReader not implemented")
}

// SurfaceFormat defines the pixel format of a texture.
type SurfaceFormat int32

const (
	SurfaceFormatColor           SurfaceFormat = 0
	SurfaceFormatDxt1            SurfaceFormat = 4
	SurfaceFormatDxt3            SurfaceFormat = 5
	SurfaceFormatDxt5            SurfaceFormat = 6
)

// Texture2D represents a 2D texture.
type Texture2D struct {
	Format        SurfaceFormat
	Width         int32
	Height        int32
	MipmapLevels  [][]byte
}

// Texture2DReader reads a Texture2D.
type Texture2DReader struct{}

func (r *Texture2DReader) Read(cr *ContentReader) (interface{}, error) {
	var format int32
	err := binary.Read(cr.reader, cr.bo, &format)
	if err != nil {
		return nil, err
	}

	var width, height, levelCount int32
	err = binary.Read(cr.reader, cr.bo, &width)
	if err != nil {
		return nil, err
	}
	err = binary.Read(cr.reader, cr.bo, &height)
	if err != nil {
		return nil, err
	}
	err = binary.Read(cr.reader, cr.bo, &levelCount)
	if err != nil {
		return nil, err
	}

	mipmapLevels := make([][]byte, levelCount)
	for i := 0; i < int(levelCount); i++ {
		var dataSize int32
		err = binary.Read(cr.reader, cr.bo, &dataSize)
		if err != nil {
			return nil, err
		}
		levelData := make([]byte, dataSize)
		_, err = io.ReadFull(cr.reader, levelData)
		if err != nil {
			return nil, err
		}
		mipmapLevels[i] = levelData
	}

	return Texture2D{
		Format:       SurfaceFormat(format),
		Width:        width,
		Height:       height,
		MipmapLevels: mipmapLevels,
	}, nil
}

// SoundEffect represents a sound effect.
type SoundEffect struct {
	Format      []byte
	Data        []byte
	LoopStart   int32
	LoopLength  int32
	DurationMS  int32
}

// SoundEffectReader reads a SoundEffect.
type SoundEffectReader struct{}

func (r *SoundEffectReader) Read(cr *ContentReader) (interface{}, error) {
	var formatSize int32
	err := binary.Read(cr.reader, cr.bo, &formatSize)
	if err != nil {
		return nil, err
	}
	format := make([]byte, formatSize)
	_, err = io.ReadFull(cr.reader, format)
	if err != nil {
		return nil, err
	}

	var dataSize int32
	err = binary.Read(cr.reader, cr.bo, &dataSize)
	if err != nil {
		return nil, err
	}
	data := make([]byte, dataSize)
	_, err = io.ReadFull(cr.reader, data)
	if err != nil {
		return nil, err
	}

	var loopStart, loopLength, durationMS int32
	err = binary.Read(cr.reader, cr.bo, &loopStart)
	if err != nil {
		return nil, err
	}
	err = binary.Read(cr.reader, cr.bo, &loopLength)
	if err != nil {
		return nil, err
	}
	err = binary.Read(cr.reader, cr.bo, &durationMS)
	if err != nil {
		return nil, err
	}

	return SoundEffect{
		Format:     format,
		Data:       data,
		LoopStart:  loopStart,
		LoopLength: loopLength,
		DurationMS: durationMS,
	}, nil
}

type Rectangle struct {
	X, Y, Width, Height int32
}

type Vector3 struct {
	X, Y, Z float32
}

type SpriteFont struct {
	Texture         Texture2D
	Glyphs          []Rectangle
	Cropping        []Rectangle
	CharMap         []rune
	LineSpacing     int32
	Spacing         float32
	Kerning         []Vector3
	DefaultCharacter *rune
}

type SpriteFontReader struct{}

func (r *SpriteFontReader) Read(cr *ContentReader) (interface{}, error) {
	texture, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	glyphs, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	cropping, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	charMap, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	var lineSpacing int32
	err = binary.Read(cr.reader, cr.bo, &lineSpacing)
	if err != nil {
		return nil, err
	}
	var spacing float32
	err = binary.Read(cr.reader, cr.bo, &spacing)
	if err != nil {
		return nil, err
	}
	kerning, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	hasDefaultChar, err := cr.ReadObject()
	if err != nil {
		return nil, err
	}
	var defaultChar *rune
	if hasDefaultChar.(bool) {
		char, err := cr.ReadObject()
		if err != nil {
			return nil, err
		}
		c := char.(rune)
		defaultChar = &c
	}

	return SpriteFont{
		Texture:         texture.(Texture2D),
		Glyphs:          glyphs.([]Rectangle),
		Cropping:        cropping.([]Rectangle),
		CharMap:         charMap.([]rune),
		LineSpacing:     lineSpacing,
		Spacing:         spacing,
		Kerning:         kerning.([]Vector3),
		DefaultCharacter: defaultChar,
	}, nil
}


func init() {
	RegisterTypeReader("Microsoft.Xna.Framework.Content.StringReader", func() ContentTypeReader { return &StringReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.Int32Reader", func() ContentTypeReader { return &Int32Reader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.BooleanReader", func() ContentTypeReader { return &BooleanReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.Vector2Reader", func() ContentTypeReader { return &Vector2Reader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.EffectReader", func() ContentTypeReader { return &EffectReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.ReflectiveReader`1", func() ContentTypeReader { return &ReflectiveReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.Texture2DReader", func() ContentTypeReader { return &Texture2DReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.SoundEffectReader", func() ContentTypeReader { return &SoundEffectReader{} })
	RegisterTypeReader("Microsoft.Xna.Framework.Content.SpriteFontReader", func() ContentTypeReader { return &SpriteFontReader{} })
}
