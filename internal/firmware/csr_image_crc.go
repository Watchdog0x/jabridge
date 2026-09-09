package firmware

// The CSR image checksum walks 16-bit words from the end, preserving byte
// order within each word, after prepending four FF bytes. It is not the
// archive's MD5 or a standard forward IEEE CRC32 over the image.
var csrImageTable = func() [256]uint32 {
	var table [256]uint32
	for i := range table {
		value := uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			high := value & 0x80000000
			value <<= 1
			if high != 0 {
				value ^= 0xdb710641
			}
		}
		table[i] = value
	}
	return table
}()

func csrImageCRC(image []byte) uint32 {
	var crc uint32
	read := func(index int) byte {
		if index < 4 {
			return 0xff
		}
		return image[index-4]
	}
	for end := len(image) + 4; end > 0; end -= 2 {
		for index := max(0, end-2); index < end; index++ {
			crc = crc<<8 ^ csrImageTable[crc>>24] ^ uint32(read(index))
		}
	}
	return crc
}
