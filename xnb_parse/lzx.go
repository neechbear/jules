package xnb_parse

import (
	"encoding/binary"
	"fmt"
	"io"
)

// LZX Constants
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


type BlockType int

const (
	BlockTypeInvalid      BlockType = 0
	BlockTypeVerbatim     BlockType = 1
	BlockTypeAligned      BlockType = 2
	BlockTypeUncompressed BlockType = 3
)

type LzxState struct {
	R0, R1, R2      uint32
	MainElements    uint16
	HeaderRead      bool
	BlockType       BlockType
	BlockLength     uint32
	BlockRemaining  uint32
	FramesRead      uint32
	IntelFilesize   int32
	IntelCurpos     int32
	IntelStarted    bool

	PretreeTable  []uint16
	PretreeLen    []byte
	MaintreeTable []uint16
	MaintreeLen   []byte
	LengthTable   []uint16
	LengthLen     []byte
	AlignedTable  []uint16
	AlignedLen    []byte

	Window     []byte
	WindowSize uint32
	WindowPosn uint32
}

type LzxDecoder struct {
	state *LzxState
}

func NewLzxDecoder(window int) *LzxDecoder {
	wndsize := uint32(1 << uint(window))
	state := &LzxState{
		Window:      make([]byte, wndsize),
		WindowSize:  wndsize,
		PretreeTable: make([]uint16, (1<<LzxPretreeTablebits)+(LzxPretreeMaxSymbols<<1)),
		PretreeLen:   make([]byte, LzxPretreeMaxSymbols+LzxLenTableSafety),
		MaintreeTable: make([]uint16, (1<<LzxMaintreeTablebits)+(LzxMaintreeMaxSymbols<<1)),
		MaintreeLen:   make([]byte, LzxMaintreeMaxSymbols+LzxLenTableSafety),
		LengthTable:   make([]uint16, (1<<LzxLengthTablebits)+(LzxLengthMaxSymbols<<1)),
		LengthLen:     make([]byte, LzxLengthMaxSymbols+LzxLenTableSafety),
		AlignedTable:  make([]uint16, (1<<LzxAlignedTablebits)+(LzxAlignedMaxSymbols<<1)),
		AlignedLen:    make([]byte, LzxAlignedMaxSymbols+LzxLenTableSafety),
	}
	for i := range state.Window {
		state.Window[i] = 0xDC
	}

	posnSlots := window << 1
	if window == 20 {
		posnSlots = 42
	} else if window == 21 {
		posnSlots = 50
	}

	state.R0, state.R1, state.R2 = 1, 1, 1
	state.MainElements = LzxNumChars + uint16(posnSlots<<3)

	for i := 0; i < LzxMaintreeMaxSymbols; i++ {
		state.MaintreeLen[i] = 0
	}
	for i := 0; i < LzxLengthMaxSymbols; i++ {
		state.LengthLen[i] = 0
	}

	return &LzxDecoder{state: state}
}

func (d *LzxDecoder) Decompress(in io.Reader, inLen int, out io.Writer, outLen int) error {
	bitbuf := NewBitBuffer(in)
	togo := outLen

	for togo > 0 {
		if d.state.BlockRemaining == 0 {
			if d.state.BlockType == BlockTypeUncompressed {
				if (d.state.BlockLength & 1) == 1 {
					// Realign bitstream to word
					bitbuf.InitBitStream()
				}
			}

			blockType, err := bitbuf.ReadBits(3)
			if err != nil {
				return err
			}
			d.state.BlockType = BlockType(blockType)

			b, err := bitbuf.ReadBits(16)
			if err != nil {
				return err
			}
			c, err := bitbuf.ReadBits(8)
			if err != nil {
				return err
			}
			d.state.BlockLength = (b << 8) | c
			d.state.BlockRemaining = d.state.BlockLength

			switch d.state.BlockType {
			case BlockTypeAligned:
				for i := 0; i < 8; i++ {
					val, err := bitbuf.ReadBits(3)
					if err != nil {
						return err
					}
					d.state.AlignedLen[i] = byte(val)
				}
				d.MakeDecodeTable(LzxAlignedMaxSymbols, LzxAlignedTablebits, d.state.AlignedLen, d.state.AlignedTable)
				fallthrough
			case BlockTypeVerbatim:
				d.ReadLengths(d.state.MaintreeLen, 0, 256, bitbuf)
				d.ReadLengths(d.state.MaintreeLen, 256, uint(d.state.MainElements), bitbuf)
				d.MakeDecodeTable(LzxMaintreeMaxSymbols, LzxMaintreeTablebits, d.state.MaintreeLen, d.state.MaintreeTable)
				if d.state.MaintreeLen[0xE8] != 0 {
					d.state.IntelStarted = true
				}
				d.ReadLengths(d.state.LengthLen, 0, LzxNumSecondaryLengths, bitbuf)
				d.MakeDecodeTable(LzxLengthMaxSymbols, LzxLengthTablebits, d.state.LengthLen, d.state.LengthTable)
			case BlockTypeUncompressed:
				d.state.IntelStarted = true
				// Align to byte boundary
				bitbuf.InitBitStream()
				var r0, r1, r2 uint32
				if err := binary.Read(bitbuf.stream, binary.LittleEndian, &r0); err != nil {
					return err
				}
				if err := binary.Read(bitbuf.stream, binary.LittleEndian, &r1); err != nil {
					return err
				}
				if err := binary.Read(bitbuf.stream, binary.LittleEndian, &r2); err != nil {
					return err
				}
				d.state.R0 = r0
				d.state.R1 = r1
				d.state.R2 = r2
			default:
				return fmt.Errorf("invalid block type")
			}
		}

		thisRun := int(d.state.BlockRemaining)
		if thisRun > togo {
			thisRun = togo
		}
		togo -= thisRun
		d.state.BlockRemaining -= uint32(thisRun)

		d.state.WindowPosn &= d.state.WindowSize - 1
		if d.state.WindowPosn+uint32(thisRun) > d.state.WindowSize {
			return fmt.Errorf("window overrun")
		}

		switch d.state.BlockType {
		case BlockTypeVerbatim, BlockTypeAligned:
			for thisRun > 0 {
				mainElement, err := d.ReadHuffSym(d.state.MaintreeTable, d.state.MaintreeLen, LzxMaintreeMaxSymbols, LzxMaintreeTablebits, bitbuf)
				if err != nil {
					return err
				}
				if mainElement < LzxNumChars {
					d.state.Window[d.state.WindowPosn] = byte(mainElement)
					d.state.WindowPosn++
					thisRun--
				} else {
					mainElement -= LzxNumChars
					matchLength := mainElement & LzxNumPrimaryLengths
					if matchLength == LzxNumPrimaryLengths {
						lengthFooter, err := d.ReadHuffSym(d.state.LengthTable, d.state.LengthLen, LzxLengthMaxSymbols, LzxLengthTablebits, bitbuf)
						if err != nil {
							return err
						}
						matchLength += lengthFooter
					}
					matchLength += LzxMinMatch
					matchOffset := uint32(mainElement >> 3)

					if d.state.BlockType == BlockTypeAligned {
						if matchOffset > 2 {
							extra := extraBits[matchOffset]
							matchOffset32 := positionBase[matchOffset] - 2
							if extra > 3 {
								extra -= 3
								verbatimBits, err := bitbuf.ReadBits(extra)
								if err != nil {
									return err
								}
								matchOffset32 += verbatimBits << 3
								alignedBits, err := d.ReadHuffSym(d.state.AlignedTable, d.state.AlignedLen, LzxAlignedMaxSymbols, LzxAlignedTablebits, bitbuf)
								if err != nil {
									return err
								}
								matchOffset32 += uint32(alignedBits)
							} else if extra == 3 {
								alignedBits, err := d.ReadHuffSym(d.state.AlignedTable, d.state.AlignedLen, LzxAlignedMaxSymbols, LzxAlignedTablebits, bitbuf)
								if err != nil {
									return err
								}
								matchOffset32 += uint32(alignedBits)
							} else if extra > 0 {
								verbatimBits, err := bitbuf.ReadBits(extra)
								if err != nil {
									return err
								}
								matchOffset32 += verbatimBits
							} else {
								matchOffset32 = 1
							}
							d.state.R2 = d.state.R1
							d.state.R1 = d.state.R0
							d.state.R0 = matchOffset32
							matchOffset = matchOffset32
						} else if matchOffset == 0 {
							matchOffset = d.state.R0
						} else if matchOffset == 1 {
							matchOffset = d.state.R1
							d.state.R1 = d.state.R0
							d.state.R0 = matchOffset
						} else {
							matchOffset = d.state.R2
							d.state.R2 = d.state.R0
							d.state.R0 = matchOffset
						}
					} else {
						if matchOffset > 2 {
							if matchOffset != 3 {
								extra := extraBits[matchOffset]
								verbatimBits, err := bitbuf.ReadBits(extra)
								if err != nil {
									return err
								}
								matchOffset = positionBase[matchOffset] - 2 + verbatimBits
							} else {
								matchOffset = 1
							}
							d.state.R2 = d.state.R1
							d.state.R1 = d.state.R0
							d.state.R0 = matchOffset
						} else if matchOffset == 0 {
							matchOffset = d.state.R0
						} else if matchOffset == 1 {
							matchOffset = d.state.R1
							d.state.R1 = d.state.R0
							d.state.R0 = matchOffset
						} else {
							matchOffset = d.state.R2
							d.state.R2 = d.state.R0
							d.state.R0 = matchOffset
						}
					}
					rundest := d.state.WindowPosn
					thisRun -= int(matchLength)
					runsrc := int(rundest) - int(matchOffset)
					if runsrc < 0 {
						runsrc += int(d.state.WindowSize)
					}
					for i := 0; i < int(matchLength); i++ {
						d.state.Window[d.state.WindowPosn] = d.state.Window[runsrc]
						d.state.WindowPosn++
						runsrc++
						if runsrc == int(d.state.WindowSize) {
							runsrc = 0
						}
					}
				}
			}

		case BlockTypeUncompressed:
			data := make([]byte, thisRun)
			_, err := io.ReadFull(bitbuf.stream, data)
			if err != nil {
				return err
			}
			copy(d.state.Window[d.state.WindowPosn:], data)
			d.state.WindowPosn += uint32(thisRun)
		default:
			return fmt.Errorf("unsupported block type")
		}
	}
	startWindowPos := int(d.state.WindowPosn)
	if startWindowPos == 0 {
		startWindowPos = int(d.state.WindowSize)
	}
	startWindowPos -= outLen
	_, err := out.Write(d.state.Window[startWindowPos : startWindowPos+outLen])
	return err
}

type BitBuffer struct {
	buffer   uint32
	bitsLeft uint8
	stream   io.Reader
}

func NewBitBuffer(stream io.Reader) *BitBuffer {
	return &BitBuffer{stream: stream}
}

func (bb *BitBuffer) InitBitStream() {
	bb.buffer = 0
	bb.bitsLeft = 0
}

func (bb *BitBuffer) EnsureBits(bits uint8) error {
	for bb.bitsLeft < bits {
		var buf [2]byte
		_, err := io.ReadFull(bb.stream, buf[:])
		if err != nil {
			return err
		}
		bb.buffer |= uint32(binary.LittleEndian.Uint16(buf[:])) << (16 - bb.bitsLeft)
		bb.bitsLeft += 16
	}
	return nil
}

func (bb *BitBuffer) PeekBits(bits uint8) uint32 {
	return bb.buffer >> (32 - bits)
}

func (bb *BitBuffer) RemoveBits(bits uint8) {
	bb.buffer <<= bits
	bb.bitsLeft -= bits
}

func (bb *BitBuffer) ReadBits(bits uint8) (uint32, error) {
	if bits == 0 {
		return 0, nil
	}
	err := bb.EnsureBits(bits)
	if err != nil {
		return 0, err
	}
	ret := bb.PeekBits(bits)
	bb.RemoveBits(bits)
	return ret, nil
}

func (d *LzxDecoder) MakeDecodeTable(nsyms, nbits uint, length []byte, table []uint16) error {
	var sym, leaf, fill, pos, tableMask, bitMask, nextSymbol uint
	bitNum := uint(1)
	tableMask = 1 << nbits
	bitMask = tableMask >> 1
	nextSymbol = bitMask

	for bitNum <= nbits {
		for sym = 0; sym < nsyms; sym++ {
			if uint(length[sym]) == bitNum {
				leaf = pos
				pos += bitMask
				if pos > tableMask {
					return fmt.Errorf("table overrun")
				}
				fill = bitMask
				for fill > 0 {
					table[leaf] = uint16(sym)
					leaf++
					fill--
				}
			}
		}
		bitMask >>= 1
		bitNum++
	}

	if pos != tableMask {
		for sym = pos; sym < tableMask; sym++ {
			table[sym] = 0
		}
		pos <<= 16
		tableMask <<= 16
		bitMask = 1 << 15

		for bitNum <= 16 {
			for sym = 0; sym < nsyms; sym++ {
				if uint(length[sym]) == bitNum {
					leaf = pos >> 16
					for fill = 0; fill < bitNum-nbits; fill++ {
						if table[leaf] == 0 {
							table[nextSymbol<<1] = 0
							table[(nextSymbol<<1)+1] = 0
							table[leaf] = uint16(nextSymbol)
							nextSymbol++
						}
						leaf = uint(table[leaf] << 1)
						if ((pos >> (15 - fill)) & 1) == 1 {
							leaf++
						}
					}
					table[leaf] = uint16(sym)
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

	if pos == tableMask {
		return nil
	}

	for sym = 0; sym < nsyms; sym++ {
		if length[sym] != 0 {
			return fmt.Errorf("erroneous table")
		}
	}
	return nil
}

func (d *LzxDecoder) ReadLengths(lens []byte, first, last uint, bitbuf *BitBuffer) error {
	for i := uint(0); i < 20; i++ {
		y, err := bitbuf.ReadBits(4)
		if err != nil {
			return err
		}
		d.state.PretreeLen[i] = byte(y)
	}
	err := d.MakeDecodeTable(LzxPretreeMaxSymbols, LzxPretreeTablebits, d.state.PretreeLen, d.state.PretreeTable)
	if err != nil {
		return err
	}

	for x := first; x < last; {
		z, err := d.ReadHuffSym(d.state.PretreeTable, d.state.PretreeLen, LzxPretreeMaxSymbols, LzxPretreeTablebits, bitbuf)
		if err != nil {
			return err
		}
		if z == 17 {
			y, err := bitbuf.ReadBits(4)
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
			y, err := bitbuf.ReadBits(5)
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
			y, err := bitbuf.ReadBits(1)
			if err != nil {
				return err
			}
			y += 4
			z, err := d.ReadHuffSym(d.state.PretreeTable, d.state.PretreeLen, LzxPretreeMaxSymbols, LzxPretreeTablebits, bitbuf)
			if err != nil {
				return err
			}
			z = uint(lens[x]) - z
			if int(z) < 0 {
				z += 17
			}
			for y > 0 {
				lens[x] = byte(z)
				x++
				y--
			}
		} else {
			z = uint(lens[x]) - z
			if int(z) < 0 {
				z += 17
			}
			lens[x] = byte(z)
			x++
		}
	}
	return nil
}

func (d *LzxDecoder) ReadHuffSym(table []uint16, lengths []byte, nsyms, nbits uint, bitbuf *BitBuffer) (uint, error) {
	err := bitbuf.EnsureBits(16)
	if err != nil {
		return 0, err
	}
	i := uint(table[bitbuf.PeekBits(byte(nbits))])
	if i >= nsyms {
		j := uint(1 << (32 - nbits))
		for {
			j >>= 1
			i <<= 1
			if (bitbuf.buffer & uint32(j)) != 0 {
				i |= 1
			}
			if j == 0 {
				return 0, fmt.Errorf("huffman table overrun")
			}
			i = uint(table[i])
			if i < nsyms {
				break
			}
		}
	}
	j := uint(lengths[i])
	bitbuf.RemoveBits(byte(j))
	return i, nil
}
