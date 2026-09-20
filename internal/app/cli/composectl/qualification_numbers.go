package composectl

import (
	"fmt"
	"strconv"
	"strings"
)

func sumQualificationIntegers(output []byte, label string) (int64, error) {
	var total int64
	for index, field := range strings.Fields(string(output)) {
		value, err := parseQualificationInteger(field, label+" entry "+strconv.Itoa(index+1))
		if err != nil {
			return 0, err
		}
		if value < 0 || value > (1<<63-1)-total {
			return 0, fmt.Errorf("%s contains an invalid file size", label)
		}
		total += value
	}
	return total, nil
}
