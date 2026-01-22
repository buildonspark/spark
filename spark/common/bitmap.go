package common

type BitMap struct {
	value []byte
	size  int
}

func NewBitMap(size int) *BitMap {
	return &BitMap{value: make([]byte, (size+7)/8), size: size}
}

func NewBitMapFromBytes(bytes []byte, size int) *BitMap {
	return &BitMap{value: bytes, size: size}
}

func (b *BitMap) Set(index int, value bool) {
	byteIndex := index / 8
	bitIndex := index % 8

	if value {
		b.value[byteIndex] |= 1 << bitIndex
	} else {
		b.value[byteIndex] &= ^(1 << bitIndex)
	}
}

func (b *BitMap) Get(index int) bool {
	byteIndex := index / 8
	bitIndex := index % 8
	return (b.value[byteIndex] & (1 << bitIndex)) != 0
}

func (b *BitMap) Bytes() []byte {
	return b.value
}

func (b *BitMap) IsAllSet() bool {
	length := len(b.value)
	for i := range length {
		if i == length-1 && b.size%8 != 0 {
			mask := byte(0xFF >> (8 - (b.size % 8)))
			if b.value[i] != mask {
				return false
			}
		} else {
			if b.value[i] != 0xFF {
				return false
			}
		}
	}
	return true
}
