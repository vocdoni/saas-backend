package internal

// Byte size units, untyped so they combine with any integer type: 16*KiB, 10*MiB.
const (
	KiB = 1 << (10 * (iota + 1))
	MiB
	GiB
)
