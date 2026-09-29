package perfsample

import "strconv"

func addrHex(addr uint64) string {
	return strconv.FormatUint(addr, 16)
}
