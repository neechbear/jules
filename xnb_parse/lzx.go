// This file is a port of the LZX decoder from FNA.
// See: https://github.com/FNA-XNA/FNA/blob/master/src/Content/LzxDecoder.cs

package xnb_parse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	LzxMinMatch            = 2
	LzxMaxMatch            = 257
	LzxNumChars            = 256
	LzxPretreeNumElements  = 20
	LzxAlignedNumElements  = 8
	LzxNumPrimaryLengths   = 7
	LzxNumSecondaryLengths = 249

	LzxPretreeMaxSymbols  = LzxPretreeNumElements
	LzxPretreeTablebits   = 6
	LzxMaintreeMaxSymbols = LzxNumChars + 50*8
	LzxMaintreeTablebits  = 12
	LzxLengthMaxSymbols   = LzxNumSecondaryLengths + 1
	LzxLengthTablebits    = 12
	LzxAlignedMaxSymbols  = LzxAlignedNumElements
	LzxAlignedTablebits   = 7

	LzxLenTableSafety = 64
)

type lzxBlockType int

const (
	lzxBlockTypeInvalid lzxBlockType = iota
	lzxBlockTypeVerbatim
	lzxBlockTypeAligned
	lzxBlockTypeUncompressed
)

var (
	positionBase []uint32
	extraBits    []byte
)

func init() {
	extraBits = make([]byte, 52)
	for i, j := 0, 0; i <= 50; i += 2 {
		extraBits[i] = byte(j)
		extraBits[i+1] = byte(j)
		if i != 0 && j < 17 {
			j++
		}
	}

	positionBase = make([]uint32, 51)
	for i, j := 0, 0; i <= 50; i++ {
		positionBase[i] = uint32(j)
		j += 1 << extraBits[i]
	}
}

type lzxState struct {
	r0, r1, r2    uint32
	mainElements  uint16
	headerRead    bool
	blockType     lzxBlockType
	blockLength   uint32
	blockRemaining uint32
	framesRead    uint32
	intelFilesize int32
	intelCurpos   int32
	intelStarted  bool

	pretreeTable []uint16
	pretreeLen   []byte
	maintreeTable []uint16
	maintreeLen  []byte
	lengthTable   []uint16
	lengthLen     []byte
	alignedTable  []uint16
	alignedLen    []byte

	window     []byte
	windowSize uint32
	windowPosn uint32
}

type LzxDecoder struct {
	state *lzxState
}

func NewLzxDecoder(window int) *LzxDecoder {
	if window < 15 || window > 21 {
		panic("Unsupported window size")
	}

	wndsize := uint32(1 << uint(window))
	var posnSlots int
	if window == 20 {
		posnSlots = 42
	} else if window == 21 {
		posnSlots = 50
	} else {
		posnSlots = window << 1
	}

	state := &lzxState{
		window:        make([]byte, wndsize),
		windowSize:    wndsize,
		r0:            1,
		r1:            1,
		r2:            1,
		mainElements:  LzxNumChars + uint16(posnSlots<<3),
		pretreeTable:  make([]uint16, (1<<LzxPretreeTablebits)+(LzxPretreeMaxSymbols<<1)),
		pretreeLen:    make([]byte, LzxPretreeMaxSymbols+LzxLenTableSafety),
		maintreeTable: make([]uint16, (1<<LzxMaintreeTablebits)+(LzxMaintreeMaxSymbols<<1)),
		maintreeLen:   make([]byte, LzxMaintreeMaxSymbols+LzxLenTableSafety),
		lengthTable:   make([]uint16, (1<<LzxLengthTablebits)+(LzxLengthMaxSymbols<<1)),
		lengthLen:     make([]byte, LzxLengthMaxSymbols+LzxLenTableSafety),
		alignedTable:  make([]uint16, (1<<LzxAlignedTablebits)+(LzxAlignedMaxSymbols<<1)),
		alignedLen:    make([]byte, LzxAlignedMaxSymbols+LzxLenTableSafety),
	}

	for i := range state.window {
		state.window[i] = 0xDC
	}
	for i := range state.maintreeLen {
		state.maintreeLen[i] = 0
	}
	for i := range state.lengthLen {
		state.lengthLen[i] = 0
	}

	return &LzxDecoder{state: state}
}

func (d *LzxDecoder) Decompress(in io.Reader, inLen int, out *bytes.Buffer, outLen int) error {
	s := d.state
	bitbuf := newBitBuffer(in)

	togo := outLen

	if !s.headerRead {
		intel, err := bitbuf.readBits(1)
		if err != nil && err != io.EOF {
			return err
		}
		if intel != 0 {
			i, err := bitbuf.readBits(16)
			if err != nil && err != io.EOF {
				return err
			}
			j, err := bitbuf.readBits(16)
			if err != nil && err != io.EOF {
				return err
			}
			s.intelFilesize = int32((i << 16) | j)
		}
		s.headerRead = true
	}

	for togo > 0 {
		if s.blockRemaining == 0 {
			if s.blockType == lzxBlockTypeUncompressed {
				if (s.blockLength & 1) != 0 {
					bitbuf.stream.Read(make([]byte, 1))
				}
				bitbuf.initBitStream()
			}

			blockType, err := bitbuf.readBits(3)
			if err != nil {
				return err
			}
			s.blockType = lzxBlockType(blockType)

			b, err := bitbuf.readBits(16)
			if err != nil {
				return err
			}
			c, err := bitbuf.readBits(8)
			if err != nil {
				return err
			}
			s.blockLength = (b << 8) | c
			s.blockRemaining = s.blockLength

			switch s.blockType {
			case lzxBlockTypeAligned:
				for i := 0; i < 8; i++ {
					val, err := bitbuf.readBits(3)
					if err != nil {
						return err
					}
					s.alignedLen[i] = byte(val)
				}
				err = makeDecodeTable(LzxAlignedMaxSymbols, LzxAlignedTablebits, s.alignedLen, s.alignedTable)
				if err != nil {
					return err
				}
				fallthrough
			case lzxBlockTypeVerbatim:
				err = d.readLengths(s.maintreeLen, 0, 256, bitbuf)
				if err != nil {
					return err
				}
				err = d.readLengths(s.maintreeLen, 256, uint32(s.mainElements), bitbuf)
				if err != nil {
					return err
				}
				err = makeDecodeTable(LzxMaintreeMaxSymbols, LzxMaintreeTablebits, s.maintreeLen, s.maintreeTable)
				if err != nil {
					return err
				}
				if s.maintreeLen[0xE8] != 0 {
					s.intelStarted = true
				}
				err = d.readLengths(s.lengthLen, 0, LzxNumSecondaryLengths, bitbuf)
				if err != nil {
					return err
				}
				err = makeDecodeTable(LzxLengthMaxSymbols, LzxLengthTablebits, s.lengthLen, s.lengthTable)
				if err != nil {
					return err
				}
			case lzxBlockTypeUncompressed:
				s.intelStarted = true
				bitbuf.initBitStream()
				var buf [12]byte
				_, err = io.ReadFull(bitbuf.stream, buf[:])
				if err != nil {
					return err
				}
				s.r0 = binary.LittleEndian.Uint32(buf[0:4])
				s.r1 = binary.LittleEndian.Uint32(buf[4:8])
				s.r2 = binary.LittleEndian.Uint32(buf[8:12])
			default:
				return fmt.Errorf("invalid block type: %d", s.blockType)
			}
		}

		thisRun := int(s.blockRemaining)
		if thisRun > togo {
			thisRun = togo
		}
		togo -= thisRun
		s.blockRemaining -= uint32(thisRun)

		switch s.blockType {
		case lzxBlockTypeVerbatim, lzxBlockTypeAligned:
			for thisRun > 0 {
				mainElement, err := d.readHuffSym(s.maintreeTable, s.maintreeLen, LzxMaintreeMaxSymbols, LzxMaintreeTablebits, bitbuf)
				if err != nil {
					return err
				}
				if mainElement < LzxNumChars {
					s.window[s.windowPosn] = byte(mainElement)
					s.windowPosn++
					if s.windowPosn == s.windowSize {
						s.windowPosn = 0
					}
					thisRun--
				} else {
					mainElement -= LzxNumChars
					matchLength := mainElement & LzxNumPrimaryLengths
					if matchLength == LzxNumPrimaryLengths {
						lengthFooter, err := d.readHuffSym(s.lengthTable, s.lengthLen, LzxLengthMaxSymbols, LzxLengthTablebits, bitbuf)
						if err != nil {
							return err
						}
						matchLength += lengthFooter
					}
					matchLength += LzxMinMatch
					matchOffset := mainElement >> 3

					if matchOffset > 2 {
						if s.blockType == lzxBlockTypeAligned {
							extra := extraBits[matchOffset]
							matchOffset = positionBase[matchOffset] - 2
							if extra > 3 {
								extra -= 3
								verbatimBits, err := bitbuf.readBits(extra)
								if err != nil {
									return err
								}
								matchOffset += verbatimBits << 3
								alignedBits, err := d.readHuffSym(s.alignedTable, s.alignedLen, LzxAlignedMaxSymbols, LzxAlignedTablebits, bitbuf)
								if err != nil {
									return err
								}
								matchOffset += alignedBits
							} else if extra == 3 {
								alignedBits, err := d.readHuffSym(s.alignedTable, s.alignedLen, LzxAlignedMaxSymbols, LzxAlignedTablebits, bitbuf)
								if err != nil {
									return err
								}
								matchOffset += alignedBits
							} else if extra > 0 {
								verbatimBits, err := bitbuf.readBits(extra)
								if err != nil {
									return err
								}
								matchOffset += verbatimBits
							} else {
								matchOffset = 1
							}
						} else {
							if matchOffset != 3 {
								extra := extraBits[matchOffset]
								verbatimBits, err := bitbuf.readBits(extra)
								if err != nil {
									return err
								}
								matchOffset = positionBase[matchOffset] - 2 + verbatimBits
							} else {
								matchOffset = 1
							}
						}
						s.r2 = s.r1
						s.r1 = s.r0
						s.r0 = matchOffset
					} else if matchOffset == 0 {
						matchOffset = s.r0
					} else if matchOffset == 1 {
						matchOffset = s.r1
						s.r1 = s.r0
						s.r0 = matchOffset
					} else {
						matchOffset = s.r2
						s.r2 = s.r0
						s.r0 = matchOffset
					}

					thisRun -= int(matchLength)

					var runsrc uint32
					if s.windowPosn >= matchOffset {
						runsrc = s.windowPosn - matchOffset
					} else {
						runsrc = s.windowPosn + (s.windowSize - matchOffset)
					}

					for matchLength > 0 {
						s.window[s.windowPosn] = s.window[runsrc]
						s.windowPosn++
						if s.windowPosn == s.windowSize {
							s.windowPosn = 0
						}
						runsrc++
						if runsrc == s.windowSize {
							runsrc = 0
						}
						matchLength--
					}
				}
			}
		case lzxBlockTypeUncompressed:
			data := make([]byte, thisRun)
			_, err := io.ReadFull(bitbuf.stream, data)
			if err != nil {
				return err
			}
			copy(s.window[s.windowPosn:], data)
			s.windowPosn += uint32(thisRun)
		}
	}

	var startWindowPos uint32
	if s.windowPosn >= uint32(outLen) {
		startWindowPos = s.windowPosn - uint32(outLen)
	} else {
		startWindowPos = s.windowSize - (uint32(outLen) - s.windowPosn)
	}

	if startWindowPos+uint32(outLen) > s.windowSize {
		toRead := s.windowSize - startWindowPos
		out.Write(s.window[startWindowPos:])
		out.Write(s.window[:outLen-int(toRead)])
	} else {
		out.Write(s.window[startWindowPos : startWindowPos+uint32(outLen)])
	}

	s.framesRead++
	return nil
}

func makeDecodeTable(nsyms uint32, nbits uint8, length []byte, table []uint16) error {
	var sym, leaf uint16
	var bitNum uint8 = 1
	var fill, pos, tableMask, bitMask, nextSymbol uint32

	pos = 0
	tableMask = 1 << nbits
	bitMask = tableMask >> 1
	nextSymbol = bitMask

	for bitNum <= nbits {
		for sym = 0; sym < uint16(nsyms); sym++ {
			if length[sym] == bitNum {
				leaf = uint16(pos)
				pos += bitMask
				if pos > tableMask {
					return fmt.Errorf("table overrun")
				}
				fill = bitMask
				for fill > 0 {
					table[leaf] = sym
					leaf++
					fill--
				}
			}
		}
		bitMask >>= 1
		bitNum++
	}

	if pos != tableMask {
		for sym = uint16(pos); sym < uint16(tableMask); sym++ {
			table[sym] = 0
		}
		pos <<= 16
		tableMask <<= 16
		bitMask = 1 << 15

		for bitNum <= 16 {
			for sym = 0; sym < uint16(nsyms); sym++ {
				if length[sym] == bitNum {
					leaf = uint16(pos >> 16)
					for fill = 0; fill < uint32(bitNum-nbits); fill++ {
						if table[leaf] == 0 {
							table[nextSymbol<<1] = 0
							table[(nextSymbol<<1)+1] = 0
							table[leaf] = uint16(nextSymbol)
							nextSymbol++
						}
						leaf = table[leaf] << 1
						if ((pos >> (15 - fill)) & 1) != 0 {
							leaf++
						}
					}
					table[leaf] = sym
					pos += bitMask
					if pos > tableMask {
						return fmt.Errorf("table overrun")
					}
				}
			}
			bitMask >>= 1
			bitNum++
		}
	}
	return nil
}

func (d *LzxDecoder) readLengths(lens []byte, first, last uint32, bitbuf *bitBuffer) error {
	var x, y uint32
	var z int32

	for x = 0; x < 20; x++ {
		y, err := bitbuf.readBits(4)
		if err != nil {
			return err
		}
		d.state.pretreeLen[x] = byte(y)
	}
	err := makeDecodeTable(LzxPretreeMaxSymbols, LzxPretreeTablebits, d.state.pretreeLen, d.state.pretreeTable)
	if err != nil {
		return err
	}

	for x = first; x < last; {
		z_uint, err := d.readHuffSym(d.state.pretreeTable, d.state.pretreeLen, LzxPretreeMaxSymbols, LzxPretreeTablebits, bitbuf)
		if err != nil {
			return err
		}
		z = int32(z_uint)
		if z == 17 {
			y, err = bitbuf.readBits(4)
			if err != nil {
				return err
			}
			y += 4
			for y > 0 {
				lens[x] = 0
				x++
				y--
			}
		} else if z == 18 {
			y, err = bitbuf.readBits(5)
			if err != nil {
				return err
			}
			y += 20
			for y > 0 {
				lens[x] = 0
				x++
				y--
			}
		} else if z == 19 {
			y, err = bitbuf.readBits(1)
			if err != nil {
				return err
			}
			y += 4
			z_uint, err := d.readHuffSym(d.state.pretreeTable, d.state.pretreeLen, LzxPretreeMaxSymbols, LzxPretreeTablebits, bitbuf)
			if err != nil {
				return err
			}
			z = int32(z_uint)
			z = int32(lens[x]) - z
			if z < 0 {
				z += 17
			}
			for y > 0 {
				lens[x] = byte(z)
				x++
				y--
			}
		} else {
			z = int32(lens[x]) - z
			if z < 0 {
				z += 17
			}
			lens[x] = byte(z)
			x++
		}
	}
	return nil
}

func (d *LzxDecoder) readHuffSym(table []uint16, lengths []byte, nsyms uint32, nbits uint8, bitbuf *bitBuffer) (uint32, error) {
	err := bitbuf.ensureBits(16)
	if err != nil && err != io.EOF {
		return 0, err
	}
	i := uint32(table[bitbuf.peekBits(nbits)])
	if i >= nsyms {
		j := uint32(1 << (32 - nbits))
		for {
			j >>= 1
			i <<= 1
			if (bitbuf.buffer & j) != 0 {
				i |= 1
			}
			if j == 0 {
				return 0, fmt.Errorf("huffman table overrun")
			}
			i = uint32(table[i])
			if i < nsyms {
				break
			}
		}
	}
	j := lengths[i]
	if j > bitbuf.bitsLeft {
		return 0, io.EOF
	}
	bitbuf.removeBits(j)
	return i, nil
}

type bitBuffer struct {
	buffer   uint32
	bitsLeft uint8
	stream   io.Reader
}

func newBitBuffer(stream io.Reader) *bitBuffer {
	return &bitBuffer{stream: stream}
}

func (bb *bitBuffer) initBitStream() {
	bb.buffer = 0
	bb.bitsLeft = 0
}

func (bb *bitBuffer) ensureBits(bits uint8) error {
	for bb.bitsLeft < bits {
		var buf [2]byte
		n, err := io.ReadFull(bb.stream, buf[:])
		if err != nil {
			if n == 1 {
				bb.buffer |= uint32(buf[0]) << (32 - 8 - bb.bitsLeft)
				bb.bitsLeft += 8
			}
			return err
		}
		val := uint32(binary.LittleEndian.Uint16(buf[:]))
		bb.buffer |= val << (32 - 16 - bb.bitsLeft)
		bb.bitsLeft += 16
	}
	return nil
}

func (bb *bitBuffer) peekBits(bits uint8) uint32 {
	return bb.buffer >> (32 - bits)
}

func (bb *bitBuffer) removeBits(bits uint8) {
	bb.buffer <<= bits
	bb.bitsLeft -= bits
}

func (bb *bitBuffer) readBits(bits uint8) (uint32, error) {
	if bits == 0 {
		return 0, nil
	}
	err := bb.ensureBits(bits)
	if err != nil && err != io.EOF {
		return 0, err
	}
	if bits > bb.bitsLeft {
		return 0, io.EOF
	}
	ret := bb.peekBits(bits)
	bb.removeBits(bits)
	return ret, nil
}
